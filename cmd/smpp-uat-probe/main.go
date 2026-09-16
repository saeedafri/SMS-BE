// Temporary UAT probe: bind to an operator and optionally send one message.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/saeedafri/sms-be/internal/connector"
)

func main() {
	addr := flag.String("addr", "", "host:port")
	to := flag.String("to", "", "msisdn in E.164 without +, empty = bind only")
	sender := flag.String("sender", "", "header")
	body := flag.String("body", "", "text")
	entity := flag.String("entity", "", "DLT PE id")
	template := flag.String("template", "", "DLT template id")
	chain := flag.String("chain", "", "DLT telemarketer chain, comma separated")
	srcTon := flag.Int("srcton", 5, "source TON")
	srcNpi := flag.Int("srcnpi", 0, "source NPI")
	dstTon := flag.Int("dstton", 1, "destination TON")
	dstNpi := flag.Int("dstnpi", 1, "destination NPI")
	wait := flag.Duration("wait", 90*time.Second, "how long to wait for a receipt")
	flag.Parse()

	config := connector.SMPPConfig{
		ConnectionID: "uat-probe", Carrier: os.Getenv("SMPP_CARRIER"), Addr: *addr,
		SystemID: os.Getenv("SMPP_USER"), Password: os.Getenv("SMPP_PASS"),
		EnquireLink: 30 * time.Second, Rebind: time.Hour, MaxTPS: 1, WindowSize: 1,
		Protocol: connector.DefaultSMPPProtocol(strings.Split(*chain, ",")),
		BindType: os.Getenv("SMPP_BIND"), SystemType: os.Getenv("SMPP_SYSTEM_TYPE"),
	}
	if *chain == "" {
		config.Protocol.Chain = ""
	}
	config.Protocol.SourceTon, config.Protocol.SourceNpi = byte(*srcTon), byte(*srcNpi)
	config.Protocol.DestTon, config.Protocol.DestNpi = byte(*dstTon), byte(*dstNpi)
	reports := make(chan connector.DeliveryReport, 4)
	events := connector.SMPPEvents{
		Report:     func(r connector.DeliveryReport) { reports <- r },
		LateSubmit: func(l connector.LateSubmit) { fmt.Printf("late submit_sm_resp: %+v\n", l) },
	}

	start := time.Now()
	bind, err := connector.DialSMPP(config, events)
	if err != nil {
		fmt.Println("BIND FAILED:", err)
		os.Exit(1)
	}
	defer bind.Close()
	fmt.Printf("BIND OK (transceiver) in %s\n", time.Since(start).Round(time.Millisecond))
	if *to == "" {
		return
	}

	receipts, err := bind.Submit(context.Background(), []connector.Submission{{
		MessageID: "uat-1", Msisdn: *to, Sender: *sender, Body: *body, Channel: "SMS",
		Country: "IN", Carrier: os.Getenv("SMPP_CARRIER"), DLTEntityID: *entity, DLTTemplateID: *template,
		Priority: true,
	}})
	fmt.Printf("SUBMIT err=%v receipts=%+v\n", err, receipts)
	if err != nil || len(receipts) == 0 || !receipts[0].Accepted {
		return
	}
	select {
	case r := <-reports:
		fmt.Printf("DELIVERY REPORT: %+v\n", r)
	case <-time.After(*wait):
		fmt.Println("no delivery report within", *wait)
	}
}
