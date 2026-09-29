package connector

import (
	"testing"
	"time"

	"github.com/linxGnu/gosmpp/pdu"
)

// Operator console: the receipt is kept as the carrier sent it (A4, A5).
func TestAReceiptKeepsTheCarriersOwnWords(t *testing.T) {
	const line = "id:4471902 sub:001 dlvrd:001 submit date:2609291000 done date:2609291000 stat:DELIVRD err:000 text:"
	receipt := pdu.NewDeliverSM().(*pdu.DeliverSM)
	receipt.EsmClass = 0x04
	receipt.Message, _ = pdu.NewShortMessage(line)

	report, ok := parseDeliveryReceipt(receipt)
	if !ok {
		t.Fatal("receipt not parsed")
	}
	if report.Stat != "DELIVRD" || report.ReceiptErr != "000" {
		t.Errorf("stat/err = %q/%q, want DELIVRD/000", report.Stat, report.ReceiptErr)
	}
	if report.Raw != line {
		t.Errorf("raw = %q, want the line untouched", report.Raw)
	}
	if report.SubmittedAt.IsZero() || report.DoneAt.Before(report.SubmittedAt) {
		t.Errorf("submitted %s, done %s", report.SubmittedAt, report.DoneAt)
	}
	want := time.Date(2026, 9, 29, 4, 30, 0, 0, time.UTC)
	if !report.SubmittedAt.Equal(want) {
		t.Errorf("submittedAt = %s, want %s (IST 10:00)", report.SubmittedAt, want)
	}
}

func TestAFailedReceiptKeepsItsReceiptErrorApartFromOurs(t *testing.T) {
	receipt := pdu.NewDeliverSM().(*pdu.DeliverSM)
	receipt.EsmClass = 0x04
	receipt.Message, _ = pdu.NewShortMessage(
		"id:77 sub:001 dlvrd:000 submit date:2609131200 done date:2609131405 stat:UNDELIV err:001 text:x")
	report, _ := parseDeliveryReceipt(receipt)
	if report.Stat != "UNDELIV" || report.ReceiptErr != "001" || report.ErrorCode != "UNDELIV:001" {
		t.Errorf("stat %q, receipt err %q, platform code %q", report.Stat, report.ReceiptErr, report.ErrorCode)
	}
}

// A one-time code's receipt echoes the start of the message in text:.
func TestBlankReceiptTextLeavesTheRestOfTheLine(t *testing.T) {
	got := BlankReceiptText("id:9 sub:001 dlvrd:001 submit date:2609291000 done date:2609291000 stat:DELIVRD err:000 text:Your OTP 48")
	want := "id:9 sub:001 dlvrd:001 submit date:2609291000 done date:2609291000 stat:DELIVRD err:000 text:"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
