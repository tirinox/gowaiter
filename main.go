// main
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/jmoiron/jsonq"
	"github.com/zenazn/goji"
	"github.com/zenazn/goji/web"
)

// ------ TIMER MODEL ------

type Timer struct {
	id     int
	tag    string
	delay  int
	active bool
	url    string
}

var counter int
var timers map[int]*Timer
var timersTags map[string]*Timer

func generateId() int {
	counter++
	return counter
}

func doTimerAction(t *Timer) {

	fmt.Printf("Timer BOOM id = %d\n", t.id)

	_, err := http.Get(t.url)
	if err == nil {
		fmt.Printf("Timer GET url %s success\n", t.url)
	} else {
		fmt.Printf("Timer GET fail; error = %s\n", err)
	}
}

func getTimerById(id int) *Timer {
	t, ok := timers[id]
	if ok {
		return t
	} else {
		return nil
	}
}

func getTimerByTag(tag string) *Timer {
	t, ok := timersTags[tag]
	if ok {
		return t
	} else {
		return nil
	}
}

func setTimer(t *Timer) {
	fmt.Printf("SetTimer id = %d for %d sec; tag = %v\n", t.id, t.delay, t.tag)
	timers[t.id] = t
	timersTags[t.tag] = t

	time.AfterFunc(time.Duration(t.delay)*time.Second, func() {
		if t.active {
			doTimerAction(t)
		}
	})
}

func deleteTimer(t *Timer) {
	t.active = false
	delete(timers, t.id)
	delete(timersTags, t.tag)
}

func initTimers() {
	counter = 0
	timers = make(map[int]*Timer)
	timersTags = make(map[string]*Timer)
}

// --------- HANDLERS ----------

type Handler func(input *jsonq.JsonQuery) interface{}

func outJSON(ok bool, id int, code int, message string) interface{} {
	var result string
	if ok {
		result = "ok"
	} else {
		result = "error"
	}
	return struct {
		Result  string `json:"result"`
		Id      int    `json:"id"`
		Message string `json:"message"`
		Code    int    `json:"code"`
	}{
		result,
		id,
		message,
		code,
	}
}

func addTimerHandler(input *jsonq.JsonQuery) interface{} {

	delay, _ := input.Int("delay")
	tag, _ := input.String("tag")
	url, _ := input.String("url")

	timer := Timer{
		id:     generateId(),
		tag:    tag,
		delay:  delay,
		active: true,
		url:    url,
	}

	setTimer(&timer)

	return struct {
		Id int `json:"id"`
	}{timer.id}
}

func deleteTimerHandler(input *jsonq.JsonQuery) interface{} {

	id, _ := input.Int("id")
	timer := getTimerById(id)

	if timer == nil {
		tag, _ := input.String("tag")
		timer = getTimerByTag(tag)
	}

	if timer == nil {
		return outJSON(false, id, 2, "timer not found")
	}

	deleteTimer(timer)

	return outJSON(true, id, 0, "timer deleted")
}

func infoHandler(input *jsonq.JsonQuery) interface{} {
	return struct {
		MC int `json:"maxCounter"`
		TA int `json:"timersActive"`
	}{
		counter,
		len(timers),
	}
}

// ----------- API ------------

func makeHandler(h Handler) web.HandlerType {
	return func(c web.C, w http.ResponseWriter, r *http.Request) {
		data := map[string]interface{}{}
		decoder := json.NewDecoder(r.Body)
		decoder.Decode(&data)
		jq := jsonq.NewQuery(data)

		fmt.Printf("request body = %v", data)

		result := h(jq)
		js, err := json.Marshal(result)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(js)
	}
}

func main() {

	flag.Set("bind", ":10025")

	initTimers()

	goji.Post("/", makeHandler(addTimerHandler))
	goji.Delete("/", makeHandler(deleteTimerHandler))
	goji.Get("/", makeHandler(infoHandler))
	goji.Serve()
}
