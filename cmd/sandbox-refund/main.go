// Command sandbox-refund gives back what tenants paid for messages the sandbox
// carried: messages that never reached a carrier, let alone a handset
// (decision D-A). It never rewrites or deletes a ledger row; it posts one
// compensating refund per tenant and currency, reason sandbox_refund.
//
// By default it only prints what it would refund. -apply posts the refunds and
// relabels each message simulated at cost 0. Each message is refunded at most
// once, so running it again refunds only what is new.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"

	"github.com/saeedafri/sms-be/internal/platform/config"
	"github.com/saeedafri/sms-be/internal/store"
)

func main() {
	apply := flag.Bool("apply", false, "post the refunds; without it nothing is written")
	flag.Parse()
	if err := run(*apply); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type group struct {
	tenantID uuid.UUID
	currency string
}

func run(apply bool) error {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	conn, err := store.NewClickHousePool(cfg.ClickHouseURL, nil).Conn(ctx)
	if err != nil {
		return err
	}

	charges, err := store.SandboxCharges(ctx, conn)
	if err != nil {
		return err
	}
	groups := map[group][]store.SandboxCharge{}
	var order []group
	for _, charge := range charges {
		key := group{charge.TenantID, charge.Currency}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], charge)
	}
	if len(order) == 0 {
		fmt.Println("no sandbox charges to refund")
		return nil
	}

	fmt.Printf("%-36s  %-8s  %8s  %14s\n", "tenant", "currency", "messages", "amount (minor)")
	for _, key := range order {
		var total int64
		for _, charge := range groups[key] {
			total += charge.AmountMinor
		}
		fmt.Printf("%-36s  %-8s  %8d  %14d\n", key.tenantID, key.currency, len(groups[key]), total)
	}
	if !apply {
		fmt.Println("\ndry run: nothing written. Run with -apply to post these refunds.")
		return nil
	}

	for _, key := range order {
		count, total, err := store.RefundSandboxCharges(ctx, pool, key.tenantID, key.currency, groups[key])
		if err != nil {
			return err
		}
		// The log follows the money: a refunded message reads simulated at
		// cost 0, as every simulated message does from now on.
		for _, charge := range groups[key] {
			if err := relabel(ctx, conn, charge); err != nil {
				return err
			}
		}
		fmt.Printf("refunded %s %s: %d message(s), %d minor units\n", key.tenantID, key.currency, count, total)
	}
	return nil
}

func relabel(ctx context.Context, conn driver.Conn, charge store.SandboxCharge) error {
	record, err := store.LoadMessageState(ctx, conn, charge.TenantID, charge.MessageID)
	if err != nil {
		return fmt.Errorf("load message %s: %w", charge.MessageID, err)
	}
	record.CostMinor, record.Simulated = 0, true
	if record.FraudFlag == "" {
		record.FraudFlag = "none"
	}
	record.Version++
	record.UpdatedAt = time.Now().UTC()
	return store.InsertMessages(ctx, conn, []store.MessageRecord{record})
}
