// main
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"time"

	"io/ioutil"
	"os"

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
var timersTags map[string]*Timer

func generateId() int {
	counter++
	return counter
}

func getUrl(url string) {
	resp, err := http.Get(url)
	if err == nil {
		fmt.Printf("Timer GET url %s success\n", url)
		resp.Body.Close()
	} else {
		fmt.Printf("Timer GET fail; error = %s\n", err)
	}
}

func doTimerAction(t *Timer) {

	fmt.Printf("Timer BOOM id = %d\n", t.id)

	getUrl(t.url)

	deleteTimer(t)
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
	timersTags[t.tag] = t
	time.AfterFunc(time.Duration(t.delay)*time.Second, func() {
		if t.active {
			doTimerAction(t)
		}
	})
}

func deleteTimer(t *Timer) {
	t.active = false
	delete(timersTags, t.tag)
}

func initTimers() {
	counter = 0
	timersTags = make(map[string]*Timer)
}

// --------- HANDLERS ----------

type Handler func(input *jsonq.JsonQuery) interface{}

func outJSON(ok bool, code int, message string) interface{} {
	var result string
	if ok {
		result = "ok"
	} else {
		result = "error"
	}
	return struct {
		Result  string `json:"result"`
		Message string `json:"message"`
		Code    int    `json:"code"`
	}{
		result,
		message,
		code,
	}
}

func addTimerHandler(input *jsonq.JsonQuery) interface{} {

	delay, _ := input.Int("delay")
	tag, _ := input.String("tag")
	url, _ := input.String("url")

	oldTimer := getTimerByTag(tag)
	if oldTimer != nil {
		deleteTimer(oldTimer)
	}

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

	tag, _ := input.String("tag")
	timer := getTimerByTag(tag)

	if timer == nil {
		return outJSON(false, 2, "timer not found")
	}

	deleteTimer(timer)

	return outJSON(true, 0, "timer deleted")
}

func infoHandler(input *jsonq.JsonQuery) interface{} {
	return struct {
		MC int `json:"maxCounter"`
		TA int `json:"timersActive"`
	}{
		counter,
		len(timersTags),
	}
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

// ----------- CRON ------------

type CronEntry struct {
	Period int    `json:"period"`
	Task   string `json:"task"`
}

func readCronConfig() []CronEntry {

	var tasks []CronEntry

	raw, err := ioutil.ReadFile("./cron.json")
	if err != nil {
		fmt.Println("can't read cron.json")
		return tasks
	}

	json.Unmarshal(raw, &tasks)
	return tasks
}

func runCron() {
	tasks := readCronConfig()
	for _, task := range tasks {
		if task.Period > 0 {
			fmt.Printf("Starting CRON task %s with period %d sec\n", task.Task, task.Period)
			ticker := time.NewTicker(time.Duration(task.Period) * time.Second)
			url := task.Task // capture by value
			go func() {
				for {
					select {
					case <- ticker.C:
						fmt.Printf("CRON task %s starting...\n", url)
						go func() {
							getUrl(url)
						}()
					}
				}
			}()
		} else {
			fmt.Printf("period for %s isn't > 0 sec\n", task.Task)
		}
	}
}

func main() {

	bind := os.Getenv("BIND")
	if bind == "" {
		bind = ":10025"
	}

	flag.Set("bind", bind)

	runCron()

	initTimers()

	goji.Post("/", makeHandler(addTimerHandler))
	goji.Delete("/", makeHandler(deleteTimerHandler))
	goji.Get("/", makeHandler(infoHandler))
	goji.Serve()
}
