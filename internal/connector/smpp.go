package connector

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/linxGnu/gosmpp"
	"github.com/linxGnu/gosmpp/data"
	"github.com/linxGnu/gosmpp/pdu"

	"github.com/saeedafri/sms-be/internal/domain/billing"
	"github.com/saeedafri/sms-be/internal/domain/compliance"
)

// The TLVs Indian operators read DLT identity from on every submit_sm.
//
// These are the tags in common use, and they are CONSTANTS TO CONFIRM against
// each operator's SMPP interface document before the first live send: public
// sources disagree about the order of 0x1400 and 0x1401, and a swapped pair
// is rejected by DLT scrubbing on every single message. 0x1402 (5122) carries
// the PE-TM chain hash TRAI has enforced since 11 December 2024.
const (
	TagDLTEntityID   pdu.Tag = 0x1400
	TagDLTTemplateID pdu.Tag = 0x1401
	TagDLTChainHash  pdu.Tag = 0x1402
)

// SMPPConfig is one operator bind, as the operator console stores it.
// Comparable on purpose: a reload keeps a live bind whose config is unchanged
// and redials only the ones that moved.
type SMPPConfig struct {
	ConnectionID string
	Carrier      string // upper case, the routes table's vocabulary
	Addr         string
	SystemID     string
	Password     string
	SystemType   string
	// BindType is transmitter, receiver or transceiver; empty means
	// transceiver. A receiver only takes delivery receipts and is never handed
	// a message to submit.
	BindType    string
	MaxTPS      int
	WindowSize  int
	EnquireLink time.Duration
	Rebind      time.Duration
}

// DLTChainHash is the value TLV 5122 carries: SHA-256 over the principal
// entity id followed by every telemarketer id in the delivery chain, joined by
// commas with no spaces, ending with the telemarketer that hands the message to
// the operator — Relay. Empty when there is nothing to hash.
func DLTChainHash(entityID string, telemarketers []string) string {
	if entityID == "" || len(telemarketers) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(append([]string{entityID}, telemarketers...), ",")))
	return fmt.Sprintf("%x", sum)
}

// SMPPEvents is what a bind tells the rest of the system after Submit has
// returned: delivery receipts, and the outcome of submits it stopped waiting for.
type SMPPEvents struct {
	Report     func(DeliveryReport)
	LateSubmit func(LateSubmit)
}

// SMPPBind is one live session to one operator.
type SMPPBind struct {
	config   SMPPConfig
	chain    []string
	events   SMPPEvents
	session  *gosmpp.Session
	ticker   *time.Ticker
	stop     chan struct{}
	stopOnce sync.Once
	window   chan struct{}
	// bulkWindow holds one fewer slot than window, so non-priority traffic can
	// never occupy every in-flight slot and an OTP always has one to use.
	//
	// With windowSize 1 it is max(0, 1) = 1 as well, so no slot is reserved and
	// an OTP waits for the one message in flight. The contract allows a window
	// of one, and at that size there is nothing to reserve.
	bulkWindow chan struct{}
	// priorityTokens and bulkTokens split the operator's TPS; see issueTokens.
	priorityTokens chan struct{}
	bulkTokens     chan struct{}

	pending sync.Map // sequence number -> *pendingSubmit
	bound   atomic.Bool
	boundAt atomic.Value // time.Time
	lastErr atomic.Value // string
}

// pendingSubmit is one submit_sm on the wire, waiting for its submit_sm_resp.
type pendingSubmit struct {
	waiter    chan pdu.PDU
	messageID string
	// ref is the operator's id for segment one, when this is a later segment.
	ref string
	// late is set once the send path stopped waiting. The response is then
	// reported through LateSubmit instead of to the waiter.
	late  atomic.Bool
	since time.Time
}

// smppResponseTimeout bounds how long one submit waits for its submit_sm_resp.
// Longer than any healthy operator takes, shorter than a caller's patience. A
// variable so a test can shorten it.
var smppResponseTimeout = 30 * time.Second

const (
	// smppDialTimeout ends a connect to a host that silently drops packets.
	// Without it the operating system decides, which is about two minutes.
	smppDialTimeout = 10 * time.Second

	// lateSubmitWindow is how long a timed-out submit keeps listening for its
	// response, and how long a message that never left the wait keeps trying.
	// The receipt window: past it the reconciler has already expired the
	// message and released its hold.
	lateSubmitWindow = 48 * time.Hour

	// bulkFloor is the share of ticks bulk traffic is guaranteed while priority
	// traffic is also waiting: one in every bulkFloor. At 5 TPS a campaign still
	// moves at 1 TPS during an OTP flood, so OTP pumping cannot silence every
	// campaign on an operator.
	bulkFloor = 5
)

// errNoSubmitResp is a submit_sm that was written and never answered. The
// operator may well have taken it.
var errNoSubmitResp = errors.New("no submit_sm_resp")

// DialSMPP binds to an operator. Receipts carry the operator's message id and
// this bind's carrier; the caller resolves them to a message.
func DialSMPP(config SMPPConfig, chain []string, events SMPPEvents) (*SMPPBind, error) {
	return dialSMPP(context.Background(), config, chain, events)
}

func dialSMPP(ctx context.Context, config SMPPConfig, chain []string, events SMPPEvents) (*SMPPBind, error) {
	if config.MaxTPS <= 0 {
		config.MaxTPS = 1
	}
	if config.WindowSize <= 0 {
		config.WindowSize = 1
	}
	if events.Report == nil {
		events.Report = func(DeliveryReport) {}
	}
	if events.LateSubmit == nil {
		events.LateSubmit = func(LateSubmit) {}
	}
	b := &SMPPBind{
		config: config, chain: chain, events: events,
		ticker:         time.NewTicker(time.Second / time.Duration(config.MaxTPS)),
		stop:           make(chan struct{}),
		window:         make(chan struct{}, config.WindowSize),
		bulkWindow:     make(chan struct{}, max(config.WindowSize-1, 1)),
		priorityTokens: make(chan struct{}),
		bulkTokens:     make(chan struct{}),
	}
	auth := gosmpp.Auth{SMSC: config.Addr, SystemID: config.SystemID,
		Password: config.Password, SystemType: config.SystemType}
	session, err := gosmpp.NewSession(smppConnector(ctx, config.BindType, auth),
		gosmpp.Settings{
			EnquireLink:      config.EnquireLink,
			ReadTimeout:      3*config.EnquireLink + 10*time.Second,
			OnAllPDU:         b.handle,
			OnReceivingError: func(err error) { b.lastErr.Store(err.Error()) },
			OnRebindingError: func(err error) { b.lastErr.Store(err.Error()) },
			OnClosed: func(gosmpp.State) {
				b.bound.Store(false)
				b.lastErr.Store("the operator closed the session")
			},
			OnRebind: func() {
				b.bound.Store(true)
				b.boundAt.Store(time.Now().UTC())
			},
		}, config.Rebind)
	if err != nil {
		b.ticker.Stop()
		b.stopOnce.Do(func() { close(b.stop) })
		return nil, fmt.Errorf("smpp %s bind %s: %w", config.Carrier, config.Addr, err)
	}
	b.session = session
	b.bound.Store(true)
	b.boundAt.Store(time.Now().UTC())
	go b.issueTokens()
	go b.forgetAbandonedSubmits()
	return b, nil
}

// issueTokens turns each TPS tick into one permission to submit.
//
// A waiting priority submit gets the tick first, so a campaign filling the bind
// delays an OTP by at most one tick — except that when bulk traffic has gone
// bulkFloor-1 ticks without one and is waiting, it gets this one.
func (b *SMPPBind) issueTokens() {
	sinceBulk := 0
	for {
		select {
		case <-b.ticker.C:
		case <-b.stop:
			return
		}
		if sinceBulk >= bulkFloor-1 {
			select {
			case b.bulkTokens <- struct{}{}:
				sinceBulk = 0
				continue
			default:
			}
		}
		select {
		case b.priorityTokens <- struct{}{}:
			sinceBulk++
			continue
		default:
		}
		select {
		case b.priorityTokens <- struct{}{}:
			sinceBulk++
		case b.bulkTokens <- struct{}{}:
			sinceBulk = 0
		case <-b.stop:
			return
		}
	}
}

// forgetAbandonedSubmits drops timed-out submits whose response never came
// within the receipt window, so the pending map cannot grow without bound.
func (b *SMPPBind) forgetAbandonedSubmits() {
	sweep := time.NewTicker(time.Hour)
	defer sweep.Stop()
	for {
		select {
		case <-sweep.C:
		case <-b.stop:
			return
		}
		cutoff := time.Now().Add(-lateSubmitWindow)
		b.pending.Range(func(key, value any) bool {
			if entry := value.(*pendingSubmit); entry.late.Load() && entry.since.Before(cutoff) {
				b.pending.Delete(key)
			}
			return true
		})
	}
}

// smppConnector opens the session the connection is configured for. Some
// operators issue a transmitter and a receiver as separate binds rather than
// one transceiver, and a bind of the wrong kind is refused at login.
func smppConnector(ctx context.Context, bindType string, auth gosmpp.Auth) gosmpp.Connector {
	dialer := func(addr string) (net.Conn, error) {
		return (&net.Dialer{Timeout: smppDialTimeout}).DialContext(ctx, "tcp", addr)
	}
	switch bindType {
	case "transmitter":
		return gosmpp.TXConnector(dialer, auth)
	case "receiver":
		return gosmpp.RXConnector(dialer, auth)
	default:
		return gosmpp.TRXConnector(dialer, auth)
	}
}

// ProbeSMPP proves a bind is accepted — credentials included — and unbinds.
// It is what the console's connection test means by "bound". It ends when the
// request does, whatever the operator's host is doing.
func ProbeSMPP(ctx context.Context, config SMPPConfig) error {
	config.Rebind = time.Hour // never rebinds: closed straight away
	if config.EnquireLink <= 0 {
		config.EnquireLink = 30 * time.Second
	}
	done := make(chan error, 1)
	go func() {
		b, err := dialSMPP(ctx, config, nil, SMPPEvents{})
		if err == nil {
			err = b.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("smpp %s bind %s: %w", config.Carrier, config.Addr, ctx.Err())
	}
}

func (b *SMPPBind) Close() error {
	b.ticker.Stop()
	b.stopOnce.Do(func() { close(b.stop) })
	b.bound.Store(false)
	return b.session.Close()
}

// handle answers every PDU the operator sends. Responses are matched to the
// submit waiting for them by sequence number; requests the operator initiates
// are acknowledged here, because an unanswered deliver_sm is resent forever and
// an unanswered enquire_link ends the bind.
func (b *SMPPBind) handle(p pdu.PDU) (pdu.PDU, bool) {
	switch v := p.(type) {
	case *pdu.SubmitSMResp, *pdu.GenericNack:
		value, ok := b.pending.LoadAndDelete(v.GetSequenceNumber())
		if !ok {
			return nil, false
		}
		entry := value.(*pendingSubmit)
		if !entry.late.Load() {
			entry.waiter <- v
			return nil, false
		}
		b.events.LateSubmit(b.lateOutcome(entry, v))
		return nil, false
	case *pdu.DeliverSM:
		if report, ok := parseDeliveryReceipt(v); ok {
			report.Carrier = b.config.Carrier
			b.events.Report(report)
		}
		return v.GetResponse(), false
	case *pdu.EnquireLink:
		return v.GetResponse(), false
	case *pdu.Unbind:
		b.bound.Store(false)
		return v.GetResponse(), true
	}
	return nil, false
}

// lateOutcome turns a response nobody was waiting for into the message's outcome.
func (b *SMPPBind) lateOutcome(entry *pendingSubmit, resp pdu.PDU) LateSubmit {
	outcome := LateSubmit{MessageID: entry.messageID, Carrier: b.config.Carrier}
	ok, isResp := resp.(*pdu.SubmitSMResp)
	if !isResp || ok.CommandStatus != data.ESME_ROK {
		outcome.ErrorCode = fmt.Sprintf("0x%08X", uint32(resp.GetHeader().CommandStatus))
		return outcome
	}
	outcome.Accepted, outcome.CarrierRef = true, entry.ref
	if outcome.CarrierRef == "" {
		outcome.CarrierRef = ok.MessageID
	}
	return outcome
}

func (b *SMPPBind) Name() string { return strings.ToLower(b.config.Carrier) }

func (b *SMPPBind) Health(context.Context) Health {
	if b.bound.Load() {
		return Health{Healthy: true, Detail: b.config.Carrier + " bound"}
	}
	detail := b.config.Carrier + " not bound"
	if last, ok := b.lastErr.Load().(string); ok && last != "" {
		detail += ": " + last
	}
	return Health{Healthy: false, Detail: detail}
}

// Submit sends each message as one submit_sm per segment, in parallel up to the
// operator's window and never faster than its TPS.
//
// A message counts as accepted only if EVERY segment was: a handset that gets
// half a message has not been sent the message. The first segment's id is the
// carrier reference, because it is the one a delivery receipt names first.
//
// A message that never left the wait for a window slot or a token gets no
// receipt at all: nothing reached the operator, so there is nothing to say yet.
// It is sent when the bind has room, and its outcome arrives through LateSubmit.
func (b *SMPPBind) Submit(ctx context.Context, submissions []Submission) ([]Receipt, error) {
	receipts := make([]*Receipt, len(submissions))
	var wg sync.WaitGroup
	for i := range submissions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			receipts[i] = b.submitOne(ctx, submissions[i])
		}(i)
	}
	wg.Wait()
	out := make([]Receipt, 0, len(submissions))
	for _, receipt := range receipts {
		if receipt != nil {
			out = append(out, *receipt)
		}
	}
	return out, nil
}

// smppClock is the time the promotional window is judged at submit. A variable
// so a test can fix it.
var smppClock = time.Now

// submitOne returns nil when the message never reached the operator and has
// been handed to sendLater.
func (b *SMPPBind) submitOne(ctx context.Context, s Submission) *Receipt {
	receipt := &Receipt{MessageID: s.MessageID}
	// The gate ran at fan-out, while the window was open. A message reaching
	// the operator after it closed is dropped by DLT scrubbing after we charged
	// for it, so the window is checked again where the message leaves.
	if s.Promotional && !compliance.PromotionalAllowedAt(s.Country, smppClock()) {
		receipt.ErrorCode = "OUTSIDE_PROMOTIONAL_WINDOW"
		return receipt
	}
	parts, err := smppParts(s, b.chain)
	if err != nil {
		receipt.ErrorCode = "ENCODING"
		return receipt
	}
	ref := ""
	for i, part := range parts {
		resp, sent, err := b.exchange(ctx, part, s.Priority, s.MessageID, ref)
		switch {
		case err != nil && !sent:
			go b.sendLater(s, parts[i:], ref)
			return nil
		case err != nil:
			// Written and unanswered: unknown, not refused. The response is
			// still listened for and reported through LateSubmit.
			receipt.ErrorCode = "SUBMIT_TIMEOUT"
			return receipt
		}
		ok, isResp := resp.(*pdu.SubmitSMResp)
		if !isResp || ok.CommandStatus != data.ESME_ROK {
			receipt.ErrorCode = fmt.Sprintf("0x%08X", uint32(resp.GetHeader().CommandStatus))
			return receipt
		}
		if i == 0 {
			ref = ok.MessageID
		}
	}
	receipt.Accepted, receipt.CarrierRef = true, ref
	return receipt
}

// sendLater finishes a message whose caller stopped waiting before it could be
// sent, and reports what the operator said.
func (b *SMPPBind) sendLater(s Submission, parts []*pdu.SubmitSM, ref string) {
	ctx, cancel := context.WithTimeout(context.Background(), lateSubmitWindow)
	defer cancel()
	go func() {
		select {
		case <-b.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	for _, part := range parts {
		resp, sent, err := b.exchange(ctx, part, s.Priority, s.MessageID, ref)
		if err != nil && sent {
			// Sent and unanswered: handle reports the late response.
			return
		}
		if err != nil {
			// Never sent, and now never will be on this bind: it closed, or the
			// window passed. Refused at cost 0, so the hold does not wait forever
			// for a receipt that cannot come.
			b.events.LateSubmit(LateSubmit{MessageID: s.MessageID, Carrier: b.config.Carrier,
				ErrorCode: "NO_OPERATOR_BIND"})
			return
		}
		ok, isResp := resp.(*pdu.SubmitSMResp)
		if !isResp || ok.CommandStatus != data.ESME_ROK {
			b.events.LateSubmit(LateSubmit{MessageID: s.MessageID, Carrier: b.config.Carrier,
				ErrorCode: fmt.Sprintf("0x%08X", uint32(resp.GetHeader().CommandStatus))})
			return
		}
		if ref == "" {
			ref = ok.MessageID
		}
	}
	b.events.LateSubmit(LateSubmit{MessageID: s.MessageID, Carrier: b.config.Carrier,
		Accepted: true, CarrierRef: ref})
}

// exchange waits for room, writes one submit_sm and waits for its response.
// sent reports whether the submit reached the socket: a wait that ends first is
// not a submit, and a timeout after writing is not a refusal.
func (b *SMPPBind) exchange(ctx context.Context, part *pdu.SubmitSM, priority bool,
	messageID, ref string) (resp pdu.PDU, sent bool, err error) {

	tokens := b.priorityTokens
	if !priority {
		tokens = b.bulkTokens
		select {
		case b.bulkWindow <- struct{}{}:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
		defer func() { <-b.bulkWindow }()
	}
	select {
	case b.window <- struct{}{}:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	defer func() { <-b.window }()
	select {
	case <-tokens:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}

	entry := &pendingSubmit{waiter: make(chan pdu.PDU, 1), messageID: messageID,
		ref: ref, since: time.Now()}
	part.AssignSequenceNumber()
	seq := part.GetSequenceNumber()
	b.pending.Store(seq, entry)
	if err := b.session.Transceiver().Submit(part); err != nil {
		b.pending.Delete(seq)
		return nil, false, err
	}
	timer := time.NewTimer(smppResponseTimeout)
	defer timer.Stop()
	select {
	case resp := <-entry.waiter:
		return resp, true, nil
	case <-timer.C:
	case <-ctx.Done():
	}
	entry.late.Store(true)
	// A response that raced the timer is already in the waiter; take it now
	// rather than lose it.
	select {
	case resp := <-entry.waiter:
		return resp, true, nil
	default:
	}
	return nil, true, errNoSubmitResp
}

// smppParts builds the submit_sm PDUs for one message: GSM 03.38 when every
// character has a GSM encoding, UCS-2 otherwise (Hindi, most emoji), split into
// concatenated segments with a UDH when it does not fit in one.
//
// The split is billing's, not the library's: what an operator receives must be
// what the customer was charged for.
func smppParts(s Submission, chain []string) ([]*pdu.SubmitSM, error) {
	texts, gsm7 := billing.Segments(s.Body)
	encoding := data.Encoding(data.UCS2)
	if gsm7 {
		encoding = data.GSM7BIT
	}
	source, err := pdu.NewAddressWithTonNpiAddr(0x05, 0x00, s.Sender) // alphanumeric header
	if err != nil {
		return nil, err
	}
	dest, err := pdu.NewAddressWithTonNpiAddr(0x01, 0x01, strings.TrimPrefix(s.Msisdn, "+"))
	if err != nil {
		return nil, err
	}

	reference := byte(time.Now().UnixNano())
	parts := make([]*pdu.SubmitSM, 0, len(texts))
	for i, text := range texts {
		part := pdu.NewSubmitSM().(*pdu.SubmitSM)
		part.SourceAddr = source
		part.DestAddr = dest
		if len(texts) == 1 {
			message, err := pdu.NewShortMessageWithEncoding(text, encoding)
			if err != nil {
				return nil, err
			}
			part.Message = message
		} else {
			encoded, err := encoding.Encode(text)
			if err != nil {
				return nil, err
			}
			if err := part.Message.SetMessageDataWithEncoding(encoded, encoding); err != nil {
				return nil, err
			}
			part.Message.SetUDH(pdu.UDH{pdu.NewIEConcatMessage(byte(len(texts)), byte(i+1), reference)})
			part.EsmClass = 0x40 // UDH present
		}
		part.RegisteredDelivery = 0x01 // a receipt for every message
		if s.DLTEntityID != "" {
			part.RegisterOptionalParam(pdu.Field{Tag: TagDLTEntityID, Data: []byte(s.DLTEntityID)})
		}
		if s.DLTTemplateID != "" {
			part.RegisterOptionalParam(pdu.Field{Tag: TagDLTTemplateID, Data: []byte(s.DLTTemplateID)})
		}
		if hash := DLTChainHash(s.DLTEntityID, chain); hash != "" {
			part.RegisterOptionalParam(pdu.Field{Tag: TagDLTChainHash, Data: []byte(hash)})
		}
		parts = append(parts, part)
	}
	return parts, nil
}

var receiptField = regexp.MustCompile(`(?i)(id|stat|err|done date):(\S*)`)

// receiptTime is the zone an operator's receipt dates are written in. SMPP 3.4
// receipt dates carry no zone; every operator this client binds to is Indian
// and writes IST.
var receiptTime = time.FixedZone("IST", 5*3600+1800)

// parseDeliveryReceipt reads the SMPP 3.4 Appendix B receipt text,
// "id:… sub:… dlvrd:… submit date:… done date:… stat:DELIVRD err:000", which
// every Indian operator sends in short_message. Not every deliver_sm is a
// receipt: esm_class bit 2 set is what makes it one, and anything else is a
// handset replying.
func parseDeliveryReceipt(d *pdu.DeliverSM) (DeliveryReport, bool) {
	if d.EsmClass&0x04 == 0 {
		return DeliveryReport{}, false
	}
	text, err := d.Message.GetMessage()
	if err != nil {
		return DeliveryReport{}, false
	}
	fields := map[string]string{}
	for _, match := range receiptField.FindAllStringSubmatch(text, -1) {
		fields[strings.ToLower(match[1])] = match[2]
	}
	if fields["id"] == "" {
		return DeliveryReport{}, false
	}
	stat := strings.ToUpper(fields["stat"])
	// The done date is when the handset got it, which can be hours before the
	// receipt reaches us. Receipt time is only the fallback.
	occurred := time.Now().UTC()
	for _, layout := range []string{"0601021504", "060102150405"} {
		if at, err := time.ParseInLocation(layout, fields["done date"], receiptTime); err == nil {
			occurred = at.UTC()
			break
		}
	}
	report := DeliveryReport{CarrierRef: fields["id"], Delivered: stat == "DELIVRD",
		OccurredAt: occurred}
	if !report.Delivered {
		report.ErrorCode = stat + ":" + fields["err"]
	}
	return report, true
}

// SMPPRouter sends each SMS over a bind for the operator its route chose, and
// passes everything else to Fallback — the sandbox, on this codebase.
//
// The sandbox serves SMS only while this deployment's environment has no
// connection rows at all. Once one exists, in any status, an SMS with no bind
// that can submit it is refused NO_OPERATOR_BIND: whether the connection is
// disabled, failed to dial, or has not been dialled yet. On a deployment with
// real operators a sandbox "delivered" is a lie a customer pays for.
type SMPPRouter struct {
	Fallback Connector

	mu         sync.RWMutex
	binds      map[string][]*SMPPBind // submitting binds, by carrier
	byID       map[string]*SMPPBind   // every bind, by connection id
	configured bool
	turn       atomic.Uint64
}

func (r *SMPPRouter) Name() string { return r.Fallback.Name() }

func (r *SMPPRouter) Health(ctx context.Context) Health {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.byID) == 0 {
		return r.Fallback.Health(ctx)
	}
	health := Health{Healthy: true}
	details := make([]string, 0, len(r.byID))
	for _, b := range r.byID {
		h := b.Health(ctx)
		health.Healthy = health.Healthy && h.Healthy
		details = append(details, h.Detail)
	}
	health.Detail = strings.Join(details, "; ")
	return health
}

// BindHealth is the live state of one connection's bind, and when it last
// bound. ok is false when the router holds no bind for it.
func (r *SMPPRouter) BindHealth(connectionID string) (health Health, boundAt time.Time, ok bool) {
	r.mu.RLock()
	bind, ok := r.byID[connectionID]
	r.mu.RUnlock()
	if !ok {
		return Health{}, time.Time{}, false
	}
	boundAt, _ = bind.boundAt.Load().(time.Time)
	return bind.Health(context.Background()), boundAt, true
}

// BoundIDs lists the connections the router currently holds a bind for.
func (r *SMPPRouter) BoundIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.byID))
	for id := range r.byID {
		ids = append(ids, id)
	}
	return ids
}

// Configured reports whether the last reload found any connection rows.
func (r *SMPPRouter) Configured() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.configured
}

// FailClosed marks the deployment configured without knowing its binds: SMS is
// refused until a reload says otherwise. For a boot that could not read its own
// configuration, which must never conclude that it has none.
func (r *SMPPRouter) FailClosed() {
	r.mu.Lock()
	r.configured = true
	r.mu.Unlock()
}

func (r *SMPPRouter) Submit(ctx context.Context, submissions []Submission) ([]Receipt, error) {
	r.mu.RLock()
	binds, configured := r.binds, r.configured
	r.mu.RUnlock()

	grouped := map[Connector][]Submission{}
	var refused []Receipt
	for _, s := range submissions {
		if s.Channel != "SMS" || !configured {
			grouped[r.Fallback] = append(grouped[r.Fallback], s)
			continue
		}
		carrierBinds := binds[strings.ToUpper(s.Carrier)]
		switch {
		case len(carrierBinds) == 0:
			refused = append(refused, Receipt{MessageID: s.MessageID, ErrorCode: "NO_OPERATOR_BIND"})
		case s.Country == "IN" && (s.DLTEntityID == "" || s.DLTTemplateID == ""):
			// Operators reject these at DLT scrubbing anyway; refusing here
			// names the cause instead of an operator error code.
			refused = append(refused, Receipt{MessageID: s.MessageID, ErrorCode: "DLT_IDS_MISSING"})
		default:
			// Round robin across the operator's sessions: operators contract
			// parallel binds, and each honours its own TPS and window.
			bind := carrierBinds[int(r.turn.Add(1)%uint64(len(carrierBinds)))]
			grouped[bind] = append(grouped[bind], s)
		}
	}

	receipts := refused
	for carrier, batch := range grouped {
		got, err := carrier.Submit(ctx, batch)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, got...)
	}
	return receipts, nil
}

// Sync makes the live binds match wanted, keyed by connection id: unchanged
// binds stay up, removed or changed ones are closed, new ones are dialled — all
// at once, so one dead host does not delay every other connection. configured
// says whether the environment has any connection rows at all.
//
// The returned map holds the dial outcome of every connection it tried to bind,
// nil for success.
func (r *SMPPRouter) Sync(wanted map[string]SMPPConfig, chain []string,
	events SMPPEvents, configured bool) map[string]error {

	r.mu.RLock()
	current := r.byID
	r.mu.RUnlock()

	next := map[string]*SMPPBind{}
	outcomes := map[string]error{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for id, config := range wanted {
		if live, ok := current[id]; ok && live.config == config && sameChain(live.chain, chain) {
			mu.Lock()
			next[id] = live
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(id string, config SMPPConfig) {
			defer wg.Done()
			bind, err := DialSMPP(config, chain, events)
			mu.Lock()
			defer mu.Unlock()
			outcomes[id] = err
			if err == nil {
				next[id] = bind
			}
		}(id, config)
	}
	wg.Wait()
	for id, live := range current {
		if next[id] != live {
			_ = live.Close()
		}
	}

	byCarrier := map[string][]*SMPPBind{}
	for _, bind := range next {
		// A receiver delivers receipts through its session and cannot submit.
		if bind.config.BindType == "receiver" {
			continue
		}
		byCarrier[bind.config.Carrier] = append(byCarrier[bind.config.Carrier], bind)
	}
	r.mu.Lock()
	r.byID, r.binds, r.configured = next, byCarrier, configured
	r.mu.Unlock()
	return outcomes
}

func sameChain(a, b []string) bool {
	return strings.Join(a, ",") == strings.Join(b, ",")
}

// SMPPAddr is the address a bind dials.
func SMPPAddr(host string, port int) string { return net.JoinHostPort(host, fmt.Sprint(port)) }
