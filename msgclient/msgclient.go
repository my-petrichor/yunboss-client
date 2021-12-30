package msgclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Client struct {
	Token           string
	Config          *Config
	QuitCh          chan *ServerQuitBody
	HeartBeatCh     chan *ServerHeartBeatBody
	LoginCh         chan *ServerLoginBody
	ClientPushCh    chan *ClientReturnBody
	ServerPushCh    chan *ServerPushBody
	HeartBeatHttpCh chan interface{}
	Trigger         chan struct{}
	Done            chan struct{}
	Conn            net.Conn
}

type HttpHeartBeatBody []struct {
	IP   string `json:"ip"`
	UID  string `json:"uid"`
	Body struct {
		Process struct {
			Nginx int `json:"nginx"`
			Php   int `json:"php"`
			Mysql int `json:"mysql"`
		} `json:"process"`
		HTTP struct {
			Disk int `json:"disk"`
		} `json:"http"`
		Shell struct {
			Network string `json:"network"`
		} `json:"shell"`
	} `json:"body"`
}

func New() *Client {
	config := &Config{}
	if err := viper.Unmarshal(config); err != nil {
		panic(err)
	}

	return &Client{
		Config:          config,
		Done:            make(chan struct{}),
		QuitCh:          make(chan *ServerQuitBody),
		HeartBeatCh:     make(chan *ServerHeartBeatBody),
		LoginCh:         make(chan *ServerLoginBody),
		ClientPushCh:    make(chan *ClientReturnBody),
		ServerPushCh:    make(chan *ServerPushBody),
		Trigger:         make(chan struct{}),
		HeartBeatHttpCh: make(chan interface{}),
	}
}

func (c *Client) Start() error {
	conn, err := net.Dial("tcp", c.Config.Msgservice)
	if err != nil {
		return err
	}
	defer conn.Close()
	c.Conn = conn
	c.Handler(conn)

	return nil
}

func (c *Client) Handler(conn net.Conn) {
	defer conn.Close()

	go c.ReceiveMsg(conn)

	for {
		var buf = make([]byte, 1024)
		n, err := conn.Read(buf)
		if err != nil {
			panic(err)
		}
		var readRequest struct {
			Type string `json:"type"`
		}
		if err = json.Unmarshal(buf[:n], &readRequest); err != nil {
			panic(err)
		}

		switch readRequest.Type {
		case "login":
			loginBody := &ServerLoginBody{}
			if err = json.Unmarshal(buf[:n], loginBody); err != nil {
				panic(err)
			}
			c.LoginCh <- loginBody

		case "quit":
			quitBody := &ServerQuitBody{}
			if err = json.Unmarshal(buf[:n], quitBody); err != nil {
				panic(err)
			}
			c.QuitCh <- quitBody

		case "heartbeat":
			heartBeatBody := &ServerHeartBeatBody{}
			if err = json.Unmarshal(buf[:n], heartBeatBody); err != nil {
				panic(err)
			}
			// c.HeartBeatCh <- heartBeatBody

		case "clientpush":
			clientPush := &ClientReturnBody{}
			if err = json.Unmarshal(buf[:n], clientPush); err != nil {
				panic(err)
			}
			c.ClientPushCh <- clientPush

		case "serverpush":
			serverPush := &ServerPushBody{}
			if err = json.Unmarshal(buf[:n], serverPush); err != nil {
				panic(err)
			}
			c.ServerPushCh <- serverPush

		default:
			fmt.Println("default-----------")
		}
	}
}

func (c *Client) Login() error {
	req := &ClientLoginBody{
		Type: "login",
		Uid:  c.Config.Uid,
		Body: "",
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := c.Conn.Write(body); err != nil {
		return err
	}

	return nil
}

func (c *Client) ReceiveMsg(conn net.Conn) {
	for {
		select {
		case receiveData := <-c.ServerPushCh:
			var req = struct {
				Body string `json:"body"`
			}{
				Body: receiveData.Body,
			}
			reqBody, err := json.Marshal(req)
			if err != nil {
				panic(err)
			}

			url := c.Config.Boss + receiveData.URL
			fmt.Println(url, "url")
			reader := bytes.NewReader(reqBody)
			request, err := http.NewRequest("POST", url, reader)
			if err != nil {
				serverReturnBody := &ServerReturnBody{
					Type:   "serverpush",
					Status: 1,
					Msg:    "url error",
					Body:   "",
				}

				d, err := json.Marshal(serverReturnBody)
				if err != nil {
					panic(err)
				}
				if _, err = c.Conn.Write(d); err != nil {
					panic(err)
				}
				continue
			}
			request.Header.Set("Content-Type", "application/json")
			client := &http.Client{Timeout: time.Second * 30}
			resp, err := client.Do(request)
			if err != nil {
				if strings.Contains(err.Error(), "Client.Timeout exceeded") {
					serverReturnBody := &ServerReturnBody{
						Type:   "serverpush",
						Status: 1,
						Msg:    "request timeout",
						Body:   "",
					}

					d, err := json.Marshal(serverReturnBody)
					if err != nil {
						panic(err)
					}
					if _, err = c.Conn.Write(d); err != nil {
						panic(err)
					}
					continue
				}
				panic(err)
			}
			defer resp.Body.Close()

			respBody, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				panic(err)
			}

			var httpResp struct{ Body string }
			if err = json.Unmarshal(respBody, &httpResp); err != nil {
				panic(err)
			}

			serverReturnBody := &ServerReturnBody{
				Type:   "serverpush",
				Status: 0,
				Msg:    "push succeed",
				Body:   httpResp.Body,
			}

			d, err := json.Marshal(serverReturnBody)
			if err != nil {
				panic(err)
			}
			if _, err = c.Conn.Write(d); err != nil {
				panic(err)
			}

		case <-c.Done:
			return
		}
	}
}

func (c *Client) HeartBeat() {
	ticker := time.NewTicker(time.Second * time.Duration(c.Config.HeartBeat))

	for {
		select {
		case <-ticker.C:
			httpHeartBeatBody := HttpHeartBeatBody{}
			if c.Config.Monitor == "" {
				fmt.Println(time.Now().Format("2006-01-02 15:04:05"))
				continue
			}

			request, err := http.NewRequest("GET", c.Config.Monitor, nil)
			if err != nil {
				panic(err)
			}
			request.Header.Set("Content-Type", "application/json")
			client := http.Client{Timeout: time.Second * 3}
			resp, err := client.Do(request)
			if err != nil {
				fmt.Println("HTTP Post timeout")
				continue
			}
			defer resp.Body.Close()
			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				panic(err)
			}

			if err = json.Unmarshal(body, &httpHeartBeatBody); err != nil {
				panic(err)
			}

			req := &ClientHeartBeatBody{
				Type:  "heartbeat",
				UID:   c.Config.Uid,
				Token: c.Token,
				Body:  httpHeartBeatBody,
			}
			data, err := json.Marshal(req)
			if err != nil {
				panic("[heartbeat] marshal error")
			}
			if _, err := c.Conn.Write(data); err != nil {
				panic("[heartbeat] write error")
			}

		case <-c.Trigger:
			httpHeartBeatBody := HttpHeartBeatBody{}
			if c.Config.Monitor == "" {
				c.HeartBeatHttpCh <- time.Now().Format("2006-01-02 15:04:05")
				continue
			}

			request, err := http.NewRequest("GET", c.Config.Monitor, nil)
			if err != nil {
				panic(err)
			}
			request.Header.Set("Content-Type", "application/json")
			client := http.Client{Timeout: time.Second * 3}
			resp, err := client.Do(request)
			if err != nil {
				if strings.Contains(err.Error(), "Client.Timeout exceeded") {
					fmt.Println("HTTP Post timeout")
					c.HeartBeatHttpCh <- "overtime"
				}
				continue
			}

			defer resp.Body.Close()
			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				panic(err)
			}

			if err = json.Unmarshal(body, &httpHeartBeatBody); err != nil {
				panic(err)
			}

			c.HeartBeatHttpCh <- httpHeartBeatBody
			req := &ClientHeartBeatBody{
				Type:  "heartbeat",
				UID:   c.Config.Uid,
				Token: c.Token,
				Body:  httpHeartBeatBody,
			}
			data, err := json.Marshal(req)
			if err != nil {
				panic("[heartbeat] marshal error")
			}
			if _, err := c.Conn.Write(data); err != nil {
				panic("[heartbeat] write error")
			}

		case <-c.Done:
			return
		}
	}
}

func (c *Client) SendMsg(msg string) error {
	req := &ClientPushBody{
		Type:  "clientpush",
		UID:   c.Config.Uid,
		Token: c.Token,
		Body:  msg,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err = c.Conn.Write(body); err != nil {
		return err
	}

	return nil
}

func (c *Client) Quit() error {
	req := &ClientQuitBody{
		Type:  "quit",
		UID:   c.Config.Uid,
		Token: c.Token,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return errors.New("[quit] marshal error")
	}
	if _, err := c.Conn.Write(data); err != nil {
		return errors.New("[quit] write error")
	}

	return nil
}