package send

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"reflect"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-resty/resty/v2"
	"github.com/spf13/cast"

	"messenger/global"
)

var (
	registered  = make(map[string]func(map[string]string) sender)
	msgCh       = make(chan *message, 10000)
	confCh      = make(chan struct{}, 1)
	name2sender = make(map[string]sender)
	rc          = resty.New()
)

type sender interface {
	send(*message) error
	getConf() map[string]string
}

type message struct {
	Sender     string         `json:"sender"`
	MsgType    string         `json:"msgtype"`
	Content    string         `json:"content"`
	Title      string         `json:"title"`
	Tos        []string       `json:"tos"`
	Ccs        []string       `json:"ccs"`
	Extra      string         `json:"extra"`
	Sync       bool           `json:"sync"`
	ContentMap map[string]any `json:"-"`
	ExtraMap   map[string]any `json:"-"`
}

func init() {
	rc.RetryCount = 3
	global.RegisterWatchCallbacks(func() {
		confCh <- struct{}{}
	})
}

func Start() error {
	for {
		select {
		case <-confCh:
			handleConfig()
		case msg := <-msgCh:
		PRIORITY:
			for {
				select {
				case <-confCh:
					fmt.Println(3)
					handleConfig()
				default:
					break PRIORITY
				}
			}
			go handleMessage(msg)
		}
	}
}

func PushMessage(ctx *gin.Context) {
	m := &message{}
	if err := ctx.ShouldBindBodyWith(&m, binding.JSON); err != nil {
		ctx.AbortWithError(http.StatusBadRequest, err)
		return
	}
	if s, ok := name2sender[m.Sender]; ok && s != nil && s.getConf()["type"] != "email" {
		if m.Content != "" {
			if err := json.Unmarshal([]byte(cast.ToString(m.Content)), &m.ContentMap); err != nil {
				ctx.AbortWithError(http.StatusBadRequest, err)
				return
			}
		}
		if m.Extra != "" {
			if err := json.Unmarshal([]byte(cast.ToString(m.Extra)), &m.ExtraMap); err != nil {
				ctx.AbortWithError(http.StatusBadRequest, err)
				return
			}
		}
	}

	if m.Sync {
		if err := handleMessage(m); err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, err)
		}
		return
	}

	msgCh <- m
}

func handleErr(info string, e error, resp *resty.Response, isOk func(dt map[string]any) bool) error {
	if e != nil {
		return e
	}

	dt := make(map[string]any)
	_ = json.Unmarshal(resp.Body(), &dt)
	if resp.StatusCode() != 200 || !isOk(dt) {
		return fmt.Errorf("%s httpcode=%v resp=%s", info, resp.StatusCode(), global.RenderPretty(dt))
	}

	return nil
}

func handleConfig() {
	confs, err := global.GetSenders()
	if err != nil {
		log.Println(err)
		return
	}

	valid := make(map[string]struct{})
	for _, conf := range confs {
		name := conf["name"]
		if s, ok := name2sender[name]; !ok || s == nil || !reflect.DeepEqual(conf, s.getConf()) {
			f, ok := registered[conf["type"]]
			if !ok || f == nil {
				continue
			}
			name2sender[name] = f(conf)
		}
		valid[name] = struct{}{}
	}

	for n := range name2sender {
		if _, ok := valid[n]; !ok {
			delete(name2sender, n)
		}
	}
}

func handleMessage(msg *message) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
		if err != nil {
			log.Println(err)
		}
	}()

	s, ok := name2sender[msg.Sender]
	if !ok {
		err = fmt.Errorf("cannot find sender with name %s", msg.Sender)
		return
	}

	if err = s.send(msg); err != nil {
		err = fmt.Errorf("send failed message=%s\nerr=%v", global.RenderPretty(msg), err)
	}

	return
}