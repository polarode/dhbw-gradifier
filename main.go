package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/yhat/scrape"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type Dualis struct {
	Client   *http.Client
	Semester []Semester
}

type Config struct {
	Username string
	Password string
}

type Semester struct {
	Name    string
	Url     string
	Modules []Module
}

type Module struct {
	Name     string
	Attempts []Attempt
}

type Attempt struct {
	Label  string
	Events []Event
}

type Event struct {
	Name  string
	Grade string // Yep, we will use strings because fuck Dualis
	Exams []Exam
}

type Exam struct {
	Semester string
	Name     string
	Grade    string
}

const (
	DEBUG           = true
	baseURL         = "https://dualis.dhbw.de/"
	loginPath       = "scripts/mgrqcgi?APPNAME=CampusNet&PRGNAME=EXTERNALPAGES&ARGUMENTS=-N000000000000001,-N000324,-Awelcome"
	loginScriptPath = "/scripts/mgrqcgi"
	userAgent       = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_12_6) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/63.0.3239.108 Safari/537.36"
)

func main() {
	// Load config file
	config, _ := parseConfig("config.json")

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return nil
		},
	}

	dualis := Dualis{
		Client: client,
	}

	homeUrl, _ := dualis.login(config.Username, config.Password)
	dualis.getResultPageList(homeUrl)

}

func parseConfig(filename string) (cfg *Config, ok bool) {
	file, err := ioutil.ReadFile(filename)
	if err != nil {
		log.Fatal("Could not open config file.")
		return nil, false
	}

	var config = Config{}

	err = json.Unmarshal(file, &config)

	if err != nil {
		fmt.Print(err)
		log.Fatal("Could not decode config file.")
		return nil, false
	}

	return &config, true
}

func (dualis *Dualis) getResultPageList(homeUrl *url.URL) {
	req, _ := http.NewRequest("GET", baseURL+homeUrl.String(), nil)
	req.Header.Add("User-Agent", userAgent)
	resp, _ := dualis.Client.Do(req)

	navElementMatcher := func(n *html.Node) bool {
		return scrape.Attr(n, "title") == "Prüfungsergebnisse"
	}

	root, _ := html.Parse(resp.Body)
	htmlNavElement, _ := scrape.Find(root, navElementMatcher)
	htmlNavLink, _ := scrape.Find(htmlNavElement, scrape.ByTag(atom.A))

	req, _ = http.NewRequest("GET", baseURL+scrape.Attr(htmlNavLink, "href"), nil)
	req.Header.Add("User-Agent", userAgent)
	resp, _ = dualis.Client.Do(req)

	root, _ = html.Parse(resp.Body)
	dualis.discoverSemesters(root)
	fmt.Println(dualis.Semester)
}

func (dualis *Dualis) discoverSemesters(root *html.Node) {
	semesterMatcher := func(n *html.Node) bool {
		return n.DataAtom == atom.Option
	}

	htmlSemesterSelect, _ := scrape.Find(root, scrape.ById("semester"))

	semesterBaseUrl := dualis.buildSemesterUrl(scrape.Attr(htmlSemesterSelect, "onchange"))

	for _, htmlSemester := range scrape.FindAllNested(htmlSemesterSelect, semesterMatcher) {
		semester := Semester{
			Name: scrape.Text(htmlSemester),
			Url:  semesterBaseUrl + scrape.Attr(htmlSemester, "value"),
		}
		dualis.Semester = append(dualis.Semester, semester)

		log.Println("Discovered new semester:", semester.Name)
	}
}

func (dualis *Dualis) buildSemesterUrl(dirt string) (url string) {
	regex := regexp.MustCompile(`(?:')(.*?)(?:')`)

	var params []string

	for _, match := range regex.FindAllStringSubmatch(dirt, -1) {
		params = append(params, match[1])
	}

	url = params[0] + "?APPNAME=" + params[1] + "&PRGNAME=" + params[2] + "&ARGUMENTS=-N" + params[3] + ",-N" + params[4] + params[5]

	return url
}

func (dualis *Dualis) login(username, password string) (homeUrl *url.URL, ok bool) {
	resp, _ := dualis.Client.Get(baseURL + loginPath)
	defer resp.Body.Close()

	_, ok = dualis.sessionCookie(resp)
	if !ok {
		log.Fatal("No session cookie configured.")
		var u *url.URL
		return u, false
	}

	u, _ := url.Parse("https://dualis.dhbw.de")
	dualis.Client.Jar.SetCookies(u, resp.Cookies())

	postData := url.Values{"usrname": {username},
		"pass":      {password},
		"APPNAME":   {"CampusNet"},
		"PRGNAME":   {"LOGINCHECK"},
		"ARGUMENTS": {"clino,usrname,pass,menuno,menu_type,browser,platform"},
		"clino":     {"000000000000001"},
		"menuno":    {"000324"},
		"menu_type": {"classic"},
		"browser":   {""},
		"platform":  {""},
	}

	req, _ := http.NewRequest("POST", baseURL+loginScriptPath, strings.NewReader(postData.Encode()))
	req.Header.Add("User-Agent", userAgent)

	resp, _ = dualis.Client.Do(req)

	if len(resp.Header.Get("REFRESH")) == 0 {
		log.Fatalln("Could not log in. Check credentials.")
		var u *url.URL
		return u, false
	} else {
		log.Println("Login successful. Following 1st startup redirect.")
	}

	refreshUrl, _ := dualis.cleanRefreshURL(resp.Header.Get("REFRESH"))
	req, _ = http.NewRequest("GET", baseURL+refreshUrl.String(), nil)
	req.Header.Add("User-Agent", userAgent)
	resp, _ = dualis.Client.Do(req)

	root, _ := html.Parse(resp.Body)
	elem, ok := scrape.Find(root, func(n *html.Node) bool {
		return n.DataAtom == atom.Meta && n.Attr[0].Key == "http-equiv" && n.Attr[0].Val == "refresh"
	})

	if !ok {
		log.Fatalln("Could not find 2nd startup redirect link.")
		var u *url.URL
		return u, false
	}

	redirectUrl, _ := dualis.cleanRefreshURL(elem.Attr[1].Val)
	log.Println("Found 2nd redirect link. Home successfully discovered.")

	return redirectUrl, true
}

func (module *Module) equal(b *Module) (same bool) {
	return cmp.Equal(module, b)
}

func (dualis *Dualis) sessionCookie(resp *http.Response) (cookie string, ok bool) {
	if len(resp.Cookies()) > 0 && resp.Cookies()[0].Name == "cnsc" {
		log.Printf("Session cookie created (%s).\n", resp.Cookies()[0].Value)
		return resp.Cookies()[0].Value, true
	} else {
		log.Println("Session cookie not created.")
		return resp.Cookies()[0].Value, false
	}
}

func (dualis *Dualis) cleanRefreshURL(dirt string) (cleanURL *url.URL, ok bool) {
	regex, _ := regexp.Compile(`\bURL=(.*)`)

	// This is probably the worst way of finding the correct string - TODO
	match := regex.FindStringSubmatch(dirt)

	cleanURL, error := url.Parse(match[1])

	if error != nil {
		var u *url.URL
		return u, false
	}

	return cleanURL, true
}

func (dualis *Dualis) parseModule(url string) (module Module, ok bool) {
	// file, _ := ioutil.ReadFile("cache/double_exam.html")
	// data := bytes.NewReader(file)
	req, _ := http.NewRequest("GET", baseURL+url, nil)
	req.Header.Add("User-Agent", userAgent)
	resp, _ := dualis.Client.Do(req)

	root, _ := html.Parse(resp.Body)

	rowMatcher := func(n *html.Node) bool {
		return n.DataAtom == atom.Tr
	}

	columnMatcher := func(n *html.Node) bool {
		return n.DataAtom == atom.Td
	}

	moduleNameMatcher := func(n *html.Node) bool {
		return n.DataAtom == atom.H1
	}

	htmlRows := scrape.FindAll(root, rowMatcher)

	htmlModuleName, _ := scrape.Find(root, moduleNameMatcher)

	module = Module{
		Name: strings.Replace(scrape.Text(htmlModuleName), "\n", "", -1),
	}

	log.Println("===============================================================")
	log.Println("MODULE: ", module.Name)
	log.Println("===============================================================")

	processingEvent := false

ProcessRows:
	for _, row := range htmlRows {
		htmlColumns := scrape.FindAll(row, columnMatcher)
		//fmt.Println(scrape.Text(row))

		switch scrape.Attr(htmlColumns[0], "class") {
		case "level01":
			log.Printf("┌ Attempt: %s\n", scrape.Text(htmlColumns[0]))

			attempt := Attempt{
				Label: scrape.Text(htmlColumns[0]),
			}
			module.Attempts = append(module.Attempts, attempt)

		case "level02":
			if processingEvent && len(htmlColumns) > 1 {
				log.Printf("└ Event result: %s\n", scrape.Text(htmlColumns[3]))

				processingEvent = false

				// Intoducing these variables to combat heavy nesting of slice keys
				//attempt := module.Attempts[len(module.Attempts)-1]
				//event := attempt.Events[len(attempt.Events)-1]

				//event.Grade = scrape.Text(htmlColumns[3])
				module.Attempts[len(module.Attempts)-1].Events[len(module.Attempts[len(module.Attempts)-1].Events)-1].Grade = scrape.Text(htmlColumns[3])
			} else {
				log.Printf("├┬ New event: %s\n", scrape.Text(htmlColumns[0]))

				processingEvent = true

				event := Event{
					Name: scrape.Text(htmlColumns[0]),
				}
				module.Attempts[len(module.Attempts)-1].Events = append(module.Attempts[len(module.Attempts)-1].Events, event)
			}

		case "tbdata":
			log.Printf("│├─ Exam: %s\n", scrape.Text(htmlColumns[1]))

			// Intoducing these variables to combat heavy nesting of slice keys
			//attempt := module.Attempts[len(module.Attempts)-1]
			//event := attempt.Events[len(attempt.Events)-1]

			exam := Exam{
				Semester: scrape.Text(htmlColumns[0]),
				Name:     scrape.Text(htmlColumns[1]),
				Grade:    scrape.Text(htmlColumns[3]),
			}

			//event.Exams = append(event.Exams, exam)
			module.Attempts[len(module.Attempts)-1].Events[len(module.Attempts[len(module.Attempts)-1].Events)-1].Exams = append(module.Attempts[len(module.Attempts)-1].Events[len(module.Attempts[len(module.Attempts)-1].Events)-1].Exams, exam)

		case "tbhead":
			if scrape.Text(htmlColumns[0]) == "Pflichtbereich" {
				break ProcessRows
			}
		}

	}
	processingEvent = false

	return module, false
}