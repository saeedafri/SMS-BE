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
	MaxTPS       int
	WindowSize   int
	EnquireLink  time.Duration
	Rebind       time.Duration
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

// SMPPBind is one live transceiver session to one operator.
type SMPPBind struct {
	config   SMPPConfig
	chain    []string
	session  *gosmpp.Session
	ticker   *time.Ticker
	window   chan struct{}
	onReport func(DeliveryReport)

	pending sync.Map // sequence number -> chan pdu.PDU
	bound   atomic.Bool
	lastErr atomic.Value // string
}

// smppResponseTimeout bounds how long one submit waits for its submit_sm_resp.
// Longer than any healthy operator takes, shorter than a caller's patience.
const smppResponseTimeout = 30 * time.Second

// DialSMPP binds to an operator. onReport receives every delivery receipt the
// operator sends on this session, carrying the operator's message id only —
// the caller resolves it to a message.
func DialSMPP(config SMPPConfig, chain []string, onReport func(DeliveryReport)) (*SMPPBind, error) {
	if config.MaxTPS <= 0 {
		config.MaxTPS = 1
	}
	if config.WindowSize <= 0 {
		config.WindowSize = 1
	}
	b := &SMPPBind{
		config: config, chain: chain, onReport: onReport,
		ticker: time.NewTicker(time.Second / time.Duration(config.MaxTPS)),
		window: make(chan struct{}, config.WindowSize),
	}
	auth := gosmpp.Auth{SMSC: config.Addr, SystemID: config.SystemID,
		Password: config.Password, SystemType: config.SystemType}
	session, err := gosmpp.NewSession(gosmpp.TRXConnector(gosmpp.NonTLSDialer, auth),
		gosmpp.Settings{
			EnquireLink: config.EnquireLink,
			ReadTimeout: 3*config.EnquireLink + 10*time.Second,
			OnAllPDU:    b.handle,
			OnReceivingError: func(err error) { b.lastErr.Store(err.Error()) },
			OnRebindingError: func(err error) { b.lastErr.Store(err.Error()) },
			OnClosed:         func(gosmpp.State) { b.bound.Store(false) },
			OnRebind:         func() { b.bound.Store(true) },
		}, config.Rebind)
	if err != nil {
		b.ticker.Stop()
		return nil, fmt.Errorf("smpp %s bind %s: %w", config.Carrier, config.Addr, err)
	}
	b.session = session
	b.bound.Store(true)
	return b, nil
}

// ProbeSMPP proves a bind is accepted — credentials included — and unbinds.
// It is what the console's connection test means by "bound".
func ProbeSMPP(config SMPPConfig) error {
	config.Rebind = time.Hour // never rebinds: closed straight away
	if config.EnquireLink <= 0 {
		config.EnquireLink = 30 * time.Second
	}
	b, err := DialSMPP(config, nil, func(DeliveryReport) {})
	if err != nil {
		return err
	}
	return b.Close()
}

func (b *SMPPBind) Close() error {
	b.ticker.Stop()
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
		if waiter, ok := b.pending.LoadAndDelete(v.GetSequenceNumber()); ok {
			waiter.(chan pdu.PDU) <- v
		}
		return nil, false
	case *pdu.DeliverSM:
		if report, ok := parseDeliveryReceipt(v); ok {
			b.onReport(report)
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
func (b *SMPPBind) Submit(ctx context.Context, submissions []Submission) ([]Receipt, error) {
	receipts := make([]Receipt, len(submissions))
	var wg sync.WaitGroup
	for i := range submissions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			receipts[i] = b.submitOne(ctx, submissions[i])
		}(i)
	}
	wg.Wait()
	return receipts, nil
}

func (b *SMPPBind) submitOne(ctx context.Context, s Submission) Receipt {
	receipt := Receipt{MessageID: s.MessageID}
	parts, err := smppParts(s, b.chain)
	if err != nil {
		receipt.ErrorCode = "ENCODING"
		return receipt
	}
	for i, part := range parts {
		resp, err := b.exchange(ctx, part)
		if err != nil {
			receipt.ErrorCode = "SUBMIT_TIMEOUT"
			return receipt
		}
		ok, isResp := resp.(*pdu.SubmitSMResp)
		if !isResp || ok.CommandStatus != data.ESME_ROK {
			receipt.ErrorCode = fmt.Sprintf("0x%08X", uint32(resp.GetHeader().CommandStatus))
			return receipt
		}
		if i == 0 {
			receipt.CarrierRef = ok.MessageID
		}
	}
	receipt.Accepted = true
	return receipt
}

func (b *SMPPBind) exchange(ctx context.Context, part *pdu.SubmitSM) (pdu.PDU, error) {
	select {
	case b.window <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-b.window }()
	select {
	case <-b.ticker.C:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	waiter := make(chan pdu.PDU, 1)
	part.AssignSequenceNumber()
	seq := part.GetSequenceNumber()
	b.pending.Store(seq, waiter)
	defer b.pending.Delete(seq)
	if err := b.session.Transceiver().Submit(part); err != nil {
		return nil, err
	}
	timer := time.NewTimer(smppResponseTimeout)
	defer timer.Stop()
	select {
	case resp := <-waiter:
		return resp, nil
	case <-timer.C:
		return nil, errors.New("no submit_sm_resp")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// smppParts builds the submit_sm PDUs for one message: GSM 03.38 when every
// character has a GSM encoding, UCS-2 otherwise (Hindi, most emoji), split into
// concatenated segments with a UDH when it does not fit in one.
func smppParts(s Submission, chain []string) ([]*pdu.SubmitSM, error) {
	encoding := data.Encoding(data.GSM7BIT)
	if _, err := data.GSM7BIT.Encode(s.Body); err != nil {
		encoding = data.UCS2
	}
	segments, err := pdu.NewLongMessageWithEncoding(s.Body, encoding)
	if err != nil {
		return nil, err
	}
	source, err := pdu.NewAddressWithTonNpiAddr(0x05, 0x00, s.Sender) // alphanumeric header
	if err != nil {
		return nil, err
	}
	dest, err := pdu.NewAddressWithTonNpiAddr(0x01, 0x01, strings.TrimPrefix(s.Msisdn, "+"))
	if err != nil {
		return nil, err
	}

	parts := make([]*pdu.SubmitSM, 0, len(segments))
	for _, segment := range segments {
		part := pdu.NewSubmitSM().(*pdu.SubmitSM)
		part.SourceAddr = source
		part.DestAddr = dest
		part.Message = *segment
		part.RegisteredDelivery = 0x01 // a receipt for every message
		if len(segments) > 1 {
			part.EsmClass = 0x40 // UDH present
		}
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

var receiptField = regexp.MustCompile(`(?i)(id|stat|err):(\S*)`)

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
	report := DeliveryReport{CarrierRef: fields["id"], Delivered: stat == "DELIVRD",
		OccurredAt: time.Now().UTC()}
	if !report.Delivered {
		report.ErrorCode = stat + ":" + fields["err"]
	}
	return report, true
}

// SMPPRouter sends each SMS over the bind for the operator its route chose, and
// passes everything else to Fallback — the sandbox, on this codebase.
//
// With no binds at all it changes nothing: every message reaches Fallback
// exactly as before. Once ANY operator is bound, an SMS whose operator has no
// bind is refused rather than handed to the sandbox, because on a deployment
// with real operators a sandbox "delivered" is a lie a customer pays for.
type SMPPRouter struct {
	Fallback Connector

	mu    sync.RWMutex
	binds map[string]*SMPPBind // by carrier
	byID  map[string]*SMPPBind // by connection id, for reloads
}

func (r *SMPPRouter) Name() string { return r.Fallback.Name() }

func (r *SMPPRouter) Health(ctx context.Context) Health {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.binds) == 0 {
		return r.Fallback.Health(ctx)
	}
	health := Health{Healthy: true}
	details := make([]string, 0, len(r.binds))
	for _, b := range r.binds {
		h := b.Health(ctx)
		health.Healthy = health.Healthy && h.Healthy
		details = append(details, h.Detail)
	}
	health.Detail = strings.Join(details, "; ")
	return health
}

func (r *SMPPRouter) Submit(ctx context.Context, submissions []Submission) ([]Receipt, error) {
	r.mu.RLock()
	binds := r.binds
	r.mu.RUnlock()

	grouped := map[Connector][]Submission{}
	var refused []Receipt
	for _, s := range submissions {
		if s.Channel != "SMS" || len(binds) == 0 {
			grouped[r.Fallback] = append(grouped[r.Fallback], s)
			continue
		}
		bind, ok := binds[strings.ToUpper(s.Carrier)]
		switch {
		case !ok:
			refused = append(refused, Receipt{MessageID: s.MessageID, ErrorCode: "NO_OPERATOR_BIND"})
		case s.Country == "IN" && (s.DLTEntityID == "" || s.DLTTemplateID == ""):
			// Operators reject these at DLT scrubbing anyway; refusing here
			// names the cause instead of an operator error code.
			refused = append(refused, Receipt{MessageID: s.MessageID, ErrorCode: "DLT_IDS_MISSING"})
		default:
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
// binds stay up, removed or changed ones are closed, new ones are dialled.
// The returned map holds the dial outcome of every connection it tried to bind,
// nil for success, so the caller can record health against each row.
func (r *SMPPRouter) Sync(wanted map[string]SMPPConfig, chain []string,
	onReport func(DeliveryReport)) map[string]error {

	r.mu.RLock()
	current := r.byID
	r.mu.RUnlock()

	next := map[string]*SMPPBind{}
	outcomes := map[string]error{}
	for id, config := range wanted {
		if live, ok := current[id]; ok && live.config == config && sameChain(live.chain, chain) {
			next[id] = live
			continue
		}
		bind, err := DialSMPP(config, chain, onReport)
		outcomes[id] = err
		if err == nil {
			next[id] = bind
		}
	}
	for id, live := range current {
		if next[id] != live {
			_ = live.Close()
		}
	}

	byCarrier := map[string]*SMPPBind{}
	for _, bind := range next {
		byCarrier[bind.config.Carrier] = bind
	}
	r.mu.Lock()
	r.byID, r.binds = next, byCarrier
	r.mu.Unlock()
	return outcomes
}

func sameChain(a, b []string) bool {
	return strings.Join(a, ",") == strings.Join(b, ",")
}

// SMPPAddr is the address a bind dials.
func SMPPAddr(host string, port int) string { return net.JoinHostPort(host, fmt.Sprint(port)) }
