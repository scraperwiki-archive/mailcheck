package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	//"code.google.com/p/rsc/imap"
	"code.google.com/p/go-imap/go1/imap"
	"github.com/kr/pretty"
)

var _ = pretty.Print

var server = flag.String("server", "imap.gmail.com", "Server to check")
var user = flag.String("user", "mailcheck@scraperwiki.com", "IMAP user")
var password = flag.String("password", "", "Mail to check")

type Message struct {
	recvd, date   time.Time
	from, subject string
}

func ParseMessage(msg *imap.Response) (time.Time, time.Time, string, string) {
	t := func(s string) string {
		s, ok := imap.Unquote(s)
		if !ok {
			panic("arg")
		}
		return s
	}

	as := msg.MessageInfo().Attrs
	recvd := t(as["INTERNALDATE"].(string))

	r, err := time.Parse("02-Jan-2006 15:04:05 -0700", recvd)
	if err != nil {
		log.Panic(err)
	}

	fs := as["ENVELOPE"].([]imap.Field)
	date := t(fs[0].(string))
	subject := t(fs[1].(string))
	// WTH, go-imap?
	recvFrom := fs[2].([]imap.Field)[0].([]imap.Field)
	from := t(recvFrom[2].(string)) + "@" + t(recvFrom[3].(string))

	d, err := time.Parse("Mon, 2 Jan 2006 15:04:05 -0700", date)
	if err != nil {
		log.Panic(err)
	}

	return r, d, from, subject
}

func FetchMessages(client *imap.Client, ids []uint32) []Message {

	set, _ := imap.NewSeqSet("")
	set.AddNum(ids...)

	cmd, err := imap.Wait(client.Fetch(set, "ENVELOPE", "INTERNALDATE", "BODY[]"))
	if err != nil {
		log.Fatal("Failed to fetch e-mails since yesterday:", err)
	}

	messages := []Message{}
	for _, msg := range cmd.Data {
		recvd, date, from, subject := ParseMessage(msg)
		messages = append(messages, Message{recvd, date, from, subject})
	}
	return messages
}

func QueryMessages(client *imap.Client, args ...string) []Message {

	imapArgs := []imap.Field{}
	for _, a := range args {
		imapArgs = append(imapArgs, a)
	}

	cmd, err := imap.Wait(client.Search(imapArgs...))
	if err != nil {
		log.Fatal("Failed to search for e-mails since yesterday:", err)
	}

	return FetchMessages(client, cmd.Data[0].SearchResults())
}

type MailHandler struct {
	Messages []Message
}

func (m *MailHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(200)
	byhost := map[string][]Message{}

	for _, msg := range m.Messages {
		x := strings.Split(msg.subject, " | ")
		if len(x) != 2 {
			continue
		}
		host, hostTime := x[0], x[1]
		_ = hostTime // second part of subject, unused here

		byhost[host] = append(byhost[host], msg)
	}

	for key, msgs := range byhost {

		w.Write([]byte("Host : " + key + "\n"))
		for _, msg := range msgs {
			x := strings.Split(msg.subject, " | ")
			if len(x) != 2 {
				continue
			}
			_, hostTime := x[0], x[1]

			w.Write([]byte(fmt.Sprintf("%16s | %v | %v | %8s\n", key, msg.recvd, hostTime, msg.recvd.Sub(msg.date))))
		}
		w.Write([]byte("\n\n\n"))
	}
}

func main() {
	defer log.Println("Done!")

	flag.Parse()

	log.Println("Connecting..")
	client, err := imap.DialTLS(*server, &tls.Config{})

	if err != nil {
		log.Fatal("Dial Error:", err)
	}

	defer client.Logout(0)
	defer client.Close(true)

	_, err = client.Login(*user, *password)
	if err != nil {
		log.Fatal("Failed to auth:", err)
	}

	_, err = imap.Wait(client.Select("[Gmail]/All Mail", false))
	if err != nil {
		log.Fatal("Failed to switch to [Gmail]/All Mail:", err)
	}

	log.Println("Querying..")

	yesterday := time.Now().Add(-25 * time.Hour)
	const layout = "02-Jan-2006"
	msgs := QueryMessages(client, "SINCE", yesterday.Format(layout))
	log.Println("Number of messages: ", len(msgs))

	go func() {
		for {
			//log.Println("switching to inbox")
			_, err = imap.Wait(client.Select("inbox", false))
			if err != nil {
				log.Fatal("Failed to switch to inbox:", err)
			}
			//log.Println("Going idle")
			_, err := client.Idle()
			if err != nil {
				panic(err)
			}
			//log.Println("waiting..")
			err = client.Recv(-1)
			if err != nil {
				// Note: this can happen if the TCP connection is reset.
				// We should probably deal with this by  restarting.
				// Presumably any of these can have that problem.
				panic(err)
			}
			_, err = client.IdleTerm()
			if err != nil {
				panic(err)
			}
			seqs := []uint32{}
			for _, resp := range client.Data {
				switch resp.Label {
				case "EXISTS":
					seqs = append(seqs, resp.Fields[0].(uint32))
				}
			}
			log.Println("New sequence numbers: ", seqs)
			if len(seqs) != 0 {
				// new email!
			}

		}
	}()

	handler := &MailHandler{msgs}

	log.Println("Serving")
	err = http.ListenAndServe(":5983", handler)
	if err != nil {
		panic(err)
	}
}

func ReportOK(cmd *imap.Command, err error) *imap.Command {
	var rsp *imap.Response
	if cmd == nil {
		fmt.Printf("--- ??? ---\n%v\n\n", err)
		panic(err)
	} else if err == nil {
		rsp, err = cmd.Result(imap.OK)
	}
	if err != nil {
		fmt.Printf("--- %s --- %q\n%v\n\n", cmd.Name(true), cmd, err)
		panic(err)
	}
	c := cmd.Client()
	fmt.Printf("--- %s ---\n"+
		"%d command response(s), %d unilateral response(s)\n"+
		"%s %s\n\n",
		cmd.Name(true), len(cmd.Data), len(c.Data), rsp.Status, rsp.Info)
	log.Println(cmd.Data, rsp.Status, rsp.Info)
	c.Data = nil
	return cmd
}
