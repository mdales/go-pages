/*
GNU GPLv3 - see LICENSE
*/

package main

import (
	"flag"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/GeertJohan/go.rice"
	"github.com/russross/blackfriday"
)

var (
	directory = "files"
	logLimit  = 5
	logLimitS = ""
	title     = "g-wiki"

	templateBox *rice.Box
)

// Node holds a Wiki node.
type Node struct {
	Title    string
	Path     string
	File     string
	Content  string
	Template string
	Revision string
	Bytes    []byte
	Dirs     []*Directory
	Log      []*Log
	Markdown template.HTML

	Edit      bool // Edit mode
	Revisions bool // Show revisions
	Author    string
	Changelog string
}

// Directory lists nodes.
type Directory struct {
	Path   string
	Name   string
	Active bool
}

// Log is an event in the past.
type Log struct {
	Hash    string
	Message string
	Time    string
	Link    bool
}

func (node *Node) isHead() bool {
	return len(node.Log) > 0 && node.Revision == node.Log[0].Hash
}

// ToMarkdown processes the node contents.
func (node *Node) ToMarkdown() {
	node.Markdown = template.HTML(string(blackfriday.MarkdownCommon(node.Bytes)))
}

// ParseBool parses a string to a bool.
func ParseBool(value string) bool {
	boolValue, err := strconv.ParseBool(value)
	if err != nil {
		return false
	}
	return boolValue
}

func wikiHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/favicon.ico" {
		return
	}
	// Params
	content := r.FormValue("content")
	changelog := r.FormValue("msg")
	author := r.FormValue("author")
	reset := r.FormValue("revert")
	revision := r.FormValue("revision")

	filePath := fmt.Sprintf("%s%s.md", directory, r.URL.Path)
	node := &Node{
		File:  r.URL.Path[1:] + ".md",
		Path:  r.URL.Path,
		Title: title,
	}
	node.Revisions = ParseBool(r.FormValue("revisions"))
	node.Edit = ParseBool(r.FormValue("edit"))

	if cookie, err := r.Cookie("author"); err == nil {
		node.Author = cookie.Value
	}

	node.Dirs = listDirectories(r.URL.Path)

	// We have content, update
	if content != "" && changelog != "" && author != "" {
		node.Author = author
		bytes := []byte(content)
		err := writeFile(bytes, filePath)
		if err != nil {
			log.Printf("Cant write to file %q, error: %v", filePath, err)
		} else {
			// Wrote file, commit
			node.Bytes = bytes
			node.GitAdd().GitCommit(changelog, author).GitLog()
			node.ToMarkdown()
		}
	} else if reset != "" {
		// Reset to revision
		node.Revision = reset
		node.GitRevert().GitCommit("Reverted to: "+node.Revision, author)
		node.Revision = ""
		node.GitShow().GitLog()
		node.ToMarkdown()
	} else {
		// Show specific revision
		node.Revision = revision
		node.GitShow().GitLog()

		createNew := len(node.Bytes) == 0
		node.Edit = node.Edit || createNew

		changelogPageName := strings.TrimLeft(node.Path, "/")
		if changelogPageName == "" {
			changelogPageName = "index page"
		}
		node.Changelog = fmt.Sprintf("Edit %s", changelogPageName)
		if createNew {
			node.Changelog = fmt.Sprintf("Create %s", changelogPageName)
		}

		if node.Edit {
			node.Content = string(node.Bytes)
			node.Template = "edit.tpl"
		} else {
			node.ToMarkdown()
		}
	}
	renderTemplate(w, node)
}

func writeFile(bytes []byte, entry string) error {
	err := os.MkdirAll(path.Dir(entry), 0777)
	if err == nil {
		return ioutil.WriteFile(entry, bytes, 0644)
	}
	return err
}

func setCookie(w http.ResponseWriter, name, value string) {
	expiration := time.Now().AddDate(1, 0, 0)
	cookie := http.Cookie{Name: name, Value: value, Expires: expiration}
	http.SetCookie(w, &cookie)
}

func renderTemplate(w http.ResponseWriter, node *Node) {

	t := template.New("wiki")
	var err error

	// Build template
	if node.Markdown != "" {
		tpl := "{{ template \"header\" . }}"

		// Show revisions
		if node.Revisions {
			tpl += "{{ template \"revisions\" . }}"
		}

		if !node.isHead() && node.Revision != "" {
			tpl += "{{ template \"revision\" . }}"
		}
		// Add node
		tpl += "{{ template \"node\" . }}"

		// Footer
		tpl += "{{ template \"footer\" . }}"
		if t, err = t.Parse(tpl); err != nil {
			log.Printf("Couldn't parse template %q: %v", tpl, err)
		}
	} else if node.Template != "" {
		tpl, err := templateBox.String(node.Template)
		if err != nil {
			log.Printf("Couldn't load template %q: %v", node.Template, err)
		} else if t, err = t.Parse(tpl); err != nil {
			log.Printf("Could not parse template %q: %v", node.Template, err)
		}
	}

	// Include the rest
	for _, name := range []string{
		"header.tpl", "footer.tpl", "revision.tpl",
		"revisions.tpl", "node.tpl",
	} {
		if tpl, err := templateBox.String(name); err != nil {
			log.Printf("Couldn't load template %q: %v", name, err)
		} else if t, err = t.Parse(tpl); err != nil {
			log.Printf("Couldn't parse template %q: %v", name, err)
		}
	}
	setCookie(w, "author", node.Author)
	if err = t.Execute(w, node); err != nil {
		log.Printf("Could not execute template: %v", err)
	}
}

func main() {
	flagDirectory := flag.String("dir", directory, "directory where the markdown files are stored")
	flagLogLimit := flag.Int("log-limit", logLimit, "log depth limit")
	flagLocal := flag.String("local", "", "serve as webserver, example: 0.0.0.0:8000")
	flagHTTP := flag.String("http", ":8000", "server as webserver, example: 0.0.0.0:8000")
	flagTitle := flag.String("title", title, "title to display")
	flag.Parse()

	addr := *flagLocal
	if addr == "" {
		addr = *flagHTTP
	}
	if addr == "" {
		return
	}
	logLimit = *flagLogLimit
	logLimitS = strconv.Itoa(logLimit)
	directory = *flagDirectory
	title = *flagTitle

	if _, err := os.Stat(directory); err != nil {
		log.Printf("WARNING: the specified directory (%q) does not exist!", directory)
	}

	// Load templates
	var err error
	templateBox, err = rice.FindBox("templates")
	if err != nil {
		log.Fatal(err)
	}

	// Static resources
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(rice.MustFindBox("static").HTTPBox())))
	// Handlers
	http.HandleFunc("/", wikiHandler)

	log.Printf("Start listening on %s.", addr)
	log.Fatalln(http.ListenAndServe(addr, nil))
}
