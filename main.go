package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"code.google.com/p/go-imap/go1/imap"
	"github.com/kr/pretty"
)

var _ = pretty.Print

var server = flag.String("server", "imap.gmail.com", "Server to check")
var user = flag.String("user", "mailcheck@scraperwiki.com", "IMAP user")
var password = flag.String("password", "", "Mail to check")
var listen_addr = flag.String("listen_addr", "0.0.0.0:5983", "Address to listen on for HTTP requests")
var frequency = flag.Duration("frequency", 1*time.Minute, "Expected frequency of email delivery")

type Message struct {
	recvd, date   time.Time
	from, subject string
	flags         imap.FlagSet
}

type Messages []Message

func (m Messages) Len() int           { return len(m) }
func (m Messages) Swap(i, j int)      { m[i], m[j] = m[j], m[i] }
func (m Messages) Less(i, j int) bool { return m[i].date.Before(m[j].date) } // order by sent datetime

func ParseMessage(msg *imap.Response) Message {
	attrs := msg.MessageInfo().Attrs

	recvTime := imap.AsDateTime(attrs["INTERNALDATE"])

	envl := imap.AsList(attrs["ENVELOPE"])
	sentTimeStr := imap.AsString(envl[0])

	sentTime, err := time.Parse("Mon, 2 Jan 2006 15:04:05 -0700", sentTimeStr)
	if err != nil {
		log.Panic(err)
	}

	subject := imap.AsString(envl[1])
	recvFrom := imap.AsList(imap.AsList(envl[2])[0])
	from := imap.AsString(recvFrom[2]) + "@" + imap.AsString(recvFrom[3])

	flags := imap.AsFlagSet(attrs["FLAGS"])

	//log.Println(from, flags)

	return Message{recvTime, sentTime, from, subject, flags}
}

func FetchMessages(client *imap.Client, ids []uint32) []Message {

	set, _ := imap.NewSeqSet("")
	set.AddNum(ids...)

	cmd, err := imap.Wait(client.Fetch(set, "ENVELOPE", "INTERNALDATE", "FLAGS"))
	if err != nil {
		log.Fatal("Failed to fetch e-mails since yesterday:", err)
	}

	messages := []Message{}
	for _, msg := range cmd.Data {
		messages = append(messages, ParseMessage(msg))
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

type HttpHandler struct {
	Messages []Message
}

func (m *HttpHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
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
		sort.Sort(Messages(msgs))

		var previous time.Time

		w.Write([]byte("Host : " + key + "\n"))
		for _, msg := range msgs {
			delivery_duration := msg.recvd.Sub(msg.date)
			gap_duration := msg.date.Sub(previous)
			// Subtract 1 because there should be at least one "frequency" and add 0.5 to round to nearest integer
			num_missed := int((gap_duration.Minutes() / frequency.Minutes()) - 1 + 0.5)
			const layout = "2006-01-02 15:04:05"
			sent := msg.date.Format(layout)
			recvd := msg.recvd.Format(layout)
			w.Write([]byte(fmt.Sprintf("%16s | %s | %s | %10s | %10s | %5d\n", key, sent, recvd, delivery_duration, gap_duration, num_missed)))
			previous = msg.date
		}
		w.Write([]byte("\n\n\n"))
	}
}

func MailClient(msgChan chan<- []Message) error {

	log.Println("Connecting..")
	client, err := imap.DialTLS(*server, &tls.Config{})

	if err != nil {
		return fmt.Errorf("Dial Error:", err)
	}

	defer client.Logout(0)
	defer client.Close(true)

	_, err = client.Login(*user, *password)
	if err != nil {
		return fmt.Errorf("Failed to auth:", err)
	}

	_, err = imap.Wait(client.Select("[Gmail]/All Mail", false))
	if err != nil {
		return fmt.Errorf("Failed to switch to [Gmail]/All Mail:", err)
	}

	//rsp, err := imap.Wait(client.Capability())

	//ReportOK(rsp, err)
	//log.Println("Caps =", rsp, err)

	//return nil

	log.Println("Querying..")

	yesterday := time.Now().Add(-25 * time.Hour)
	const layout = "02-Jan-2006"
	msgs := QueryMessages(client, "SINCE", yesterday.Format(layout))

	msgChan <- msgs
	log.Println("Number of messages:", len(msgs))

	for {
		//log.Println("switching to inbox")
		_, err = imap.Wait(client.Select("inbox", false))
		if err != nil {
			return fmt.Errorf("Failed to switch to inbox: %q", err)
		}
		//client.Send("NOTIFY", ...)
		/*
			N := "notify set status (selected MessageNew (uid)) (subtree Lists MessageNew)"
			rsp, err := imap.Wait(client.Send(N))
			log.Println("Rsp =", rsp)
			ReportOK(rsp, err)
			log.Println("Rsp =", rsp)
		*/
		//log.Println("Going idle")
		_, err = client.Idle()
		if err != nil {
			return fmt.Errorf("Client idle: %q", err)
		}
		//log.Println("waiting..")
		// Blocks until new mail arrives
		err = client.Recv(-1)
		if err != nil {
			// Note: this can happen if the TCP connection is reset.
			// We should probably deal with this by  restarting.
			// Presumably any of these can have that problem.
			return fmt.Errorf("Recv: %q", err)
		}
		_, err = client.IdleTerm()
		if err != nil {
			return fmt.Errorf("IdleTerm: %q", err)
		}
		seqs := []uint32{}
		for _, resp := range client.Data {
			switch resp.Label {
			case "EXISTS":
				seqs = append(seqs, imap.AsNumber(resp.Fields[0]))
			}
		}
		log.Println("New sequence numbers: ", seqs)
		if len(seqs) != 0 {
			// new email!
			msgChan <- FetchMessages(client, seqs)
			set, _ := imap.NewSeqSet("")
			set.AddNum(seqs...)
			cmd, err := client.Store(set, "+FLAGS.SILENT", imap.NewFlagSet(`\Seen`))
			if err != nil {
				panic(err)
			}
			_, err = cmd.Result(imap.OK)
			if err != nil {
				panic(err)
			}
			/*
				_, err = imap.Wait(client.Expunge(set))
				if err != nil {
					return fmt.Errorf("EXPUNGE: %q", err)
				}
			*/
		}
	}
}

func main() {
	defer log.Println("Done!")

	flag.Parse()

	msgsChan := make(chan []Message)
	go func() {
		for {
			err := MailClient(msgsChan)
			if err != nil {
				log.Println("MailClient() Error: ", err)
			}
			time.Sleep(5 * time.Minute)
		}
	}()

	handler := &HttpHandler{Messages: []Message{}}

	go func() {
		// update handler.Messages
		for msgs := range msgsChan {
			handler.Messages = append(handler.Messages, msgs...)
		}
	}()

	log.Printf("Serving on http://%s", *listen_addr)
	err := http.ListenAndServe(*listen_addr, handler)
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
