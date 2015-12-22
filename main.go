// main
package main

import (
	"encoding/json"
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
}

var counter int
var timers map[int]*Timer
var timersTags map[string]*Timer

func generateId() int {
	counter++
	return counter
}

func doTimerAction(t *Timer) {
	// todo!
	fmt.Printf("BOOM id = %d\n", t.id)
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
	fmt.Printf("SetTimer id = %d for %d sec\n", t.id, t.delay)
	timers[t.id] = t
	timersTags[t.tag] = t
	go func(t *Timer) {
		time.Sleep(time.Duration(t.delay) * time.Second)
		if t.active {
			doTimerAction(t)
		}
	}(t)
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

	timer := Timer{
		id:     generateId(),
		tag:    tag,
		delay:  delay,
		active: true,
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

// ----------- API ------------

func makeHandler(h Handler) web.HandlerType {
	return func(c web.C, w http.ResponseWriter, r *http.Request) {
		data := map[string]interface{}{}
		decoder := json.NewDecoder(r.Body)
		decoder.Decode(&data)
		jq := jsonq.NewQuery(data)
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

	initTimers()

	goji.Post("/", makeHandler(addTimerHandler))
	goji.Delete("/", makeHandler(deleteTimerHandler))
	goji.Serve()
}
