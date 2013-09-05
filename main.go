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

	"github.com/scraperwiki/mailcheck/go-imap/go1/imap"
)

var server = flag.String("server", "imap.gmail.com", "Server to check")
var user = flag.String("user", "mailcheck@scraperwiki.com", "IMAP user")
var password = flag.String("password", "", "Mail to check")
var listen_addr = flag.String("listen_addr", "0.0.0.0:5983", "Address to listen on for HTTP requests")
var frequency = flag.Duration("frequency", 1*time.Minute, "Expected frequency of email delivery")
var time_period = flag.Duration("time_period", 49*time.Hour, "Amount of historic email to fetch (days)")

const time_layout = "2006-01-02 15:04:05"

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
	//# resp := bufio.NewWriter(w)
	// defer resp.Flush()
	s := time.Now()

	R := func(format string, args ...interface{}) {
		fmt.Fprintf(w, format, args...)
	}

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

	lines := []string{}
	for key, msgs := range byhost {
		lines = append(lines, "Host : "+key+"\n\n")

		sort.Sort(Messages(msgs))

		var previous time.Time

		oldest_missed_message := time.Now()

		total_missed := 0
		for i, msg := range msgs {
			delivery_duration := msg.recvd.Sub(msg.date)
			gap_duration := msg.date.Sub(previous)
			// Subtract 1 because there should be at least one "frequency" and add 0.5 to round to nearest integer
			num_missed := int((gap_duration.Minutes() / frequency.Minutes()) - 1 + 0.5)
			if i == 0 {
				num_missed = 0
				gap_duration = 0
			}
			total_missed += num_missed
			if num_missed > 0 && msg.date.Before(oldest_missed_message) {
				oldest_missed_message = msg.date
			}
			sent := msg.date.Format(time_layout)
			recvd := msg.recvd.Format(time_layout)
			var lateness time.Duration
			if num_missed != 0 {
				lateness = time.Since(msg.date)
			}
			line := fmt.Sprintf("%16s | %s | %s | %10s | %10s | %5d | %v\n",
				key, sent, recvd, delivery_duration, gap_duration, num_missed, lateness)
			lines = append(lines, line)
			previous = msg.date
		}

		//
		// Criterial for health of email system
		//
		state := "GOOD"
		if oldest_missed_message.Before(time.Now().Add(-24 * time.Hour)) {
			state = "BAD"
		}

		w.Write([]byte("Host : " + key + "\n"))
		R("Total messages missed: %d\n", total_missed)
		R("Total messages: %d\n", len(msgs))
		R("Oldest missed messages: %s\n", oldest_missed_message.Format(time_layout))
		R("State %s: %s", key, state)
		w.Write([]byte("\n\n\n"))
	}
	R(strings.Join(lines, ""))
	R("\n")
	R("Generation time: %s\n", time.Since(s))
}

// Fetch the last day's worth of e-mails and then idle waiting for push
// notifications from the IMAP server to tell us that there is new mail.
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

	log.Println("Querying..")

	// Fetch the last <time unit> days worth of e-mails from "All Mail".
	date_from := time.Now().Add(-1 * (*time_period))
	const layout = "02-Jan-2006"
	msgs := QueryMessages(client, "SINCE", date_from.Format(layout))

	msgChan <- nil
	msgChan <- msgs
	log.Println("Number of messages:", len(msgs))

	// TODO(pwaller): Refactor the error handling code, a lot.

	for {
		//log.Println("switching to inbox")
		_, err = imap.Wait(client.Select("inbox", false))
		if err != nil {
			return fmt.Errorf("Failed to switch to inbox: %q", err)
		}
		//log.Println("Going idle")
		_, err := client.Idle()
		if err != nil {
			return fmt.Errorf("Client idle: %q", err)
		}
		//log.Println("waiting..")

		// Blocks until new mail arrives
		// Wait a maximum of five minutes before giving up
		err = client.Recv(5 * time.Minute)
		if err != nil && err != imap.ErrTimeout {
			// Note: this can happen if the TCP connection is reset.
			// We should probably deal with this by  restarting.
			// Presumably any of these can have that problem.
			return fmt.Errorf("Recv: %q", err)
		}

		// We can't do anything until we exit the idle state
		cmd, err := client.IdleTerm()
		if err != nil {
			return fmt.Errorf("IdleTerm: %q", err)
		}
		_, err = cmd.Result(imap.OK)
		if err != nil {
			return fmt.Errorf("IdleTerm: %q", err)
		}
		seqs := []uint32{}
		for _, resp := range client.Data {
			// The IMAP server can send us a load of different types of
			// unsolicited commands but we only care about "EXISTS" which
			// indicates there is new mail that is not marked \Seen
			switch resp.Label {
			case "EXISTS":
				seqs = append(seqs, imap.AsNumber(resp.Fields[0]))
			}
		}
		// Clear client data so that we don't see the same sequence numbers
		// repeatedly
		client.Data = nil

		log.Printf("%d new messages", len(seqs))

		if len(seqs) != 0 {
			// New e-mail! Fetch, parse, and send them to who asked for them.
			msgChan <- FetchMessages(client, seqs)
		}
	}
}

func main() {
	defer log.Println("Done!")

	flag.Parse()

	msgsChan := make(chan []Message)

	// The top-level mail client go-routine.
	go func() {
		for {
			err := MailClient(msgsChan)
			if err != nil {
				log.Printf("MailClient failed, trying again in 5m: %q", err)
			}
			// Back off for a reasonable length of time before trying to
			// connect to the IMAP server again.
			time.Sleep(5 * time.Minute)
		}
	}()

	handler := &HttpHandler{Messages: []Message{}}

	go func() {
		// Routine responsible for updating handler.Messages
		// TODO(pwaller): HttpHandler really should use a sync.RWMutex
		//                otherwise it's possible the http client sees a partial
		//                state
		for msgs := range msgsChan {
			if msgs == nil {
				// Signal to clear the handler.Messages list
				handler.Messages = []Message{}
				continue
			}
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
