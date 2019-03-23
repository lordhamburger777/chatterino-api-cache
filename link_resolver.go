package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/gorilla/mux"
)

type LinkResolverResponse struct {
	Status  int    `json:"status"`
	Message string `json:"message,omitempty"`

	Tooltip string `json:"tooltip,omitempty"`
	Link    string `json:"link,omitempty"`

	// Flag in the BTTV API to.. maybe signify that the link will download something? idk
	// Download *bool  `json:"download,omitempty"`
}

var noLinkInfoFound = &LinkResolverResponse{
	Status:  404,
	Message: "No link info found",
}

var invalidURL = &LinkResolverResponse{
	Status:  500,
	Message: "Invalid URL",
}

func unescapeURLArgument(r *http.Request, key string) (string, error) {
	vars := mux.Vars(r)
	escapedURL := vars[key]
	url, err := url.PathUnescape(escapedURL)
	if err != nil {
		return "", err
	}

	return url, nil
}

func formatDuration(dur string) string {
	dur = strings.ToLower(dur)
	dur = strings.Replace(dur, "pt", "", 1)
	d, _ := time.ParseDuration(dur)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

func insertCommas(str string, n int) string {
	var buffer bytes.Buffer
	var remainder = n - 1
	var lenght = len(str) - 2
	for i, rune := range str {
		buffer.WriteRune(rune)
		if (lenght-i)%n == remainder {
			buffer.WriteRune(',')
		}
	}
	return buffer.String()
}

var linkResolverRequestsMutex sync.Mutex
var linkResolverRequests = make(map[string][](chan interface{}))

type customURLManager struct {
	check func(resp *http.Response) bool
	run   func(resp *http.Response) ([]byte, error)
}

var (
	customURLManagers []customURLManager
)

func doRequest(url string) {
	response := cacheGetOrSet("url:"+url, 10*time.Minute, func() (interface{}, error) {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		// ensures websites return pages in english (e.g. twitter would return french preview
		// when the request came from a french IP.)
		req.Header.Add("Accept-Language", "en-US, en;q=0.9, *;q=0.5")

		resp, err := httpClient.Do(req)
		if err != nil {
			if strings.HasSuffix(err.Error(), "no such host") {
				return json.Marshal(noLinkInfoFound)
			}

			return json.Marshal(&LinkResolverResponse{Status: 500, Message: "client.Get " + err.Error()})
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			doc, err := goquery.NewDocumentFromReader(resp.Body)
			if err != nil {
				return json.Marshal(&LinkResolverResponse{Status: 500, Message: "html parser error " + err.Error()})
			}

			for _, m := range customURLManagers {
				if m.check(resp) {
					return m.run(resp)
				}
			}

			escapedTitle := doc.Find("title").First().Text()
			if escapedTitle != "" {
				escapedTitle = fmt.Sprintf("<b>%s</b><hr>", html.EscapeString(escapedTitle))
			}
			return json.Marshal(&LinkResolverResponse{
				Status:  resp.StatusCode,
				Tooltip: fmt.Sprintf("<div style=\"text-align: left;\">%s<b>URL:</b> %s</div>", escapedTitle, html.EscapeString(resp.Request.URL.String())),
				Link:    resp.Request.URL.String(),
			})
		}

		return json.Marshal(noLinkInfoFound)
	})

	linkResolverRequestsMutex.Lock()
	fmt.Println("Notify channels")
	for _, channel := range linkResolverRequests[url] {
		fmt.Printf("Notify channel %v\n", channel)
		/*
			select {
			case channel <- response:
				fmt.Println("hehe")
			default:
				fmt.Println("Unable to respond")
			}
		*/
		channel <- response
	}
	delete(linkResolverRequests, url)
	linkResolverRequestsMutex.Unlock()
}

func linkResolver(w http.ResponseWriter, r *http.Request) {
	url, err := unescapeURLArgument(r, "url")
	if err != nil {
		bytes, err := json.Marshal(invalidURL)
		if err != nil {
			fmt.Println("Error marshalling invalidURL struct:", err)
			return
		}
		_, err = w.Write(bytes)
		if err != nil {
			fmt.Println("Error in w.Write:", err)
		}
		return
	}

	cacheKey := "url:" + url

	var response interface{}

	if data := cacheGet(cacheKey); data != nil {
		response = data
	} else {
		responseChannel := make(chan interface{})

		linkResolverRequestsMutex.Lock()
		linkResolverRequests[url] = append(linkResolverRequests[url], responseChannel)
		urlRequestsLength := len(linkResolverRequests[url])
		linkResolverRequestsMutex.Unlock()
		if urlRequestsLength == 1 {
			// First poll for this URL, start the request!
			go doRequest(url)
		}

		fmt.Printf("Listening to channel %v\n", responseChannel)
		response = <-responseChannel
		fmt.Println("got response!")
	}

	_, err = w.Write(response.([]byte))
	if err != nil {
		fmt.Println("Error in w.Write:", err)
	}
}
