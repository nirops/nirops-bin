package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var homeUrl = "https://api.nirops.com"

var scheme = "wss"
var addr *string = nil
var token = ""
var cloudOutputUrl = ""
var heartbeatUrl = ""

func initUrls(cloudUrl string) {
	host := strings.Split(cloudUrl, "/")[2]
	log.Printf("Connecting to %s", host)
	if strings.Contains(host, "8080") {
		scheme = "ws"
	}
	addr = flag.String("addr", host , "http service address")
	token = initToken()
	cloudOutputUrl = fmt.Sprintf("%s/wsapi/command", cloudUrl)
	heartbeatUrl = fmt.Sprintf("%s/wsapi/session-check", cloudUrl)
}
type Command struct {
	Id string `json:"id"`
	Command string `json:"command"`
	ChannelId string `json:"channel_id"`
}
type WsHeartbeat struct {
	Hostname string `json:"hostname"`
	Ts string `json:"ts"`
	Token string `json:"token"`
}

func initToken() string {
	usr, _ := user.Current()
	dir := usr.HomeDir
	body, err := ioutil.ReadFile(dir + "/.nirops/nirops.yml")
	if err != nil {
		log.Fatalf("unable to read file: %v", err)
	}
	content := strings.Split(string(body), ":")[1]
	return strings.TrimSpace(content)
}

func sendOutputToCloud(id string, cid string, postBody string) {
	postBody = strings.ReplaceAll(postBody, "\n", "\\n")
	responseBody := bytes.NewBuffer([]byte(postBody))
	req, err := http.NewRequest("POST", fmt.Sprintf(cloudOutputUrl+"/%s/channel/%s", id, cid), responseBody)
	if err != nil {
		log.Fatalf("An Error Occured %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("token", token);
	//req.Header.Set("Content-Encoding", "gzip")
	//resp, err := http.Post(fmt.Sprintf(cloudOutputUrl+"/%s", id), "application/json", responseBody)
	resp, nerr := http.DefaultClient.Do(req)
	if nerr != nil {
		log.Fatalf("An Error Occured %v", nerr)
	}
	defer resp.Body.Close()
}

func sendHeartbeatToCloud() (int, string){
	hostname, herr := os.Hostname()
	if herr != nil {
		hostname = "Unknown Host"
	}
	ts := time.Now().UnixNano() / int64(time.Millisecond)
	respJson := fmt.Sprintf(`{"hostname": "%s","ts":"%d"}`, hostname, ts)
	responseBody := bytes.NewBuffer([]byte(respJson))
	req, err := http.NewRequest("POST", fmt.Sprintf(heartbeatUrl), responseBody)
	if err != nil {
		log.Fatalf("An Error Occured %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("token", token);
	resp, nerr := http.DefaultClient.Do(req)
	if nerr != nil {
		log.Fatalf("An Error Occured %v", nerr)
	}
	defer resp.Body.Close()
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	bodyString := string(bodyBytes)
	return resp.StatusCode, bodyString
}

func stopWsClient(c *websocket.Conn) {
	if c == nil {
		return
	}
	err := c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	if err != nil {
		log.Println("write close:", err)
		return
	}
}

func startWsClient() *websocket.Conn {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	u := url.URL{Scheme: scheme, Host: *addr, Path: "/"}
	log.Printf("connecting to %s", u.String())

	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatal("dial:", err)
	}
	defer c.Close()

	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			_, message, err := c.ReadMessage()
			if err != nil {
				log.Println("read:", err)
				return
			}
			log.Printf("recv: %s", message)
			handleMessge(message)
		}
	}()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			break
		case t := <-ticker.C:
			if c == nil {
				return nil
			}
			hostname, _ := os.Hostname()
			hBeatPtr := &WsHeartbeat{
				Hostname:   hostname,
				Token: token,
				Ts: t.String()}
			hBeatJson, _ := json.Marshal(hBeatPtr)
			fmt.Println()
			err := c.WriteMessage(websocket.TextMessage, []byte(string(hBeatJson)))
			if err != nil {
				log.Println("write:", err)
				return nil
			}
			break
		case <-interrupt:
			log.Println("WS Client interrupt")

			// Cleanly close the connection by sending a close message and then
			// waiting (with timeout) for the server to close the connection.
			stopWsClient(c)
			select {
			case <-done:
			case <-time.After(time.Second):
			}
			return nil
		}
	}

	return c
}

func handleMessge(message []byte) {
	var cmd Command
	err := json.Unmarshal(message, &cmd)
	if err != nil {
		log.Println("Bad Json:", err)
		return
	}

	log.Printf("Received command: %s", cmd.Command);
	out, cmderr := exec.Command("bash", "-c", cmd.Command).Output()

	if cmderr != nil {
		log.Println(cmderr.Error())
		out = []byte("Bad Command")
	}
	resp := strings.TrimSuffix(string(out),"\n")
	resp = strings.ReplaceAll(resp, "\"", "\\\"")
	respJson := fmt.Sprintf(`{"id": "%s","output": "%s"}`, cmd.Id, resp)
	sendOutputToCloud(cmd.Id, cmd.ChannelId, respJson)
}

func main() {
	flag.Parse()
	log.SetFlags(0)
	if len(os.Args) > 1 {
		argname := os.Args[1]
		if argname == "authtoken" {
			saveToken(os.Args[2])
			return
		}
		homeUrl = os.Args[1]
	}
	initUrls(homeUrl)
	startHeartbeat()
}

func makeDirectoryIfNotExists(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return os.Mkdir(path, os.ModeDir|0755)
	}
	return nil
}

func saveToken(token string) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatal( err )
	}

	tokenDir := homeDir + "/.nirops"

	makeDirectoryIfNotExists(homeDir + "/.nirops")
	saveTokenToFile(token, tokenDir + "/nirops.yml")
}

func saveTokenToFile(token string, file string) {
	fileHandle, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		log.Fatal("Failed to write token")
	}
	fileHandle.WriteString("authtoken: " + token + "\n")
	fileHandle.Close()
}

func startHeartbeat() {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var c *websocket.Conn
	log.Println("Waiting for command....")
	for {
		select {
		case _ = <-ticker.C:
			code, resp := sendHeartbeatToCloud()
			if code == 200 {
				c = startWsClient()
			}
			if code == 201 {
				handleMessge([]byte(resp))
			}
		case <-interrupt:
			log.Println("interrupt Heartbeat")
			stopWsClient(c)
			select {
			case <-time.After(time.Second):
			}
			return
		}
	}
}
