package api

import (
	"time"

	"github.com/NakliTechie/ferrule/internal/app"
	"github.com/NakliTechie/ferrule/internal/i18n"
	"github.com/NakliTechie/ferrule/internal/store"
)

func sinceMillis(hours int) int64 {
	if hours <= 0 {
		return 0
	}
	return time.Now().Add(-time.Duration(hours) * time.Hour).UnixMilli()
}

// usageReport is the per-app / per-model ledger view: spend, volume, latency, errors.
func usageReport(a *app.App, by []string, sinceHours int) (any, error) {
	since := sinceMillis(sinceHours)
	buckets, err := a.DB.Aggregate(by, since)
	if err != nil {
		return nil, err
	}
	total := store.Bucket{Key: "total"}
	for _, b := range buckets {
		total.Requests += b.Requests
		total.Errors += b.Errors
		total.PromptTokens += b.PromptTokens
		total.CompletionTokens += b.CompletionTokens
		total.Cost += b.Cost
		total.ReqBytes += b.ReqBytes
		total.RespBytes += b.RespBytes
	}
	return map[string]any{
		"by": by, "since_hours": sinceHours, "buckets": buckets, "total": total,
		"empty_message": i18n.T("usage.empty"),
	}, nil
}

// egressReport answers the question this dashboard exists for: what left the machine?
// Cost views are everywhere; this one is not, and it is the headline (§1.4).
func egressReport(a *app.App, sinceHours int) (any, error) {
	since := sinceMillis(sinceHours)
	split, err := a.DB.Aggregate([]string{"egress"}, since)
	if err != nil {
		return nil, err
	}
	detail, err := a.DB.Aggregate([]string{"egress", "provider", "model"}, since)
	if err != nil {
		return nil, err
	}
	var local, cloud store.Bucket
	for _, b := range split {
		switch b.Egress {
		case store.EgressLocal:
			local = b
		case store.EgressCloud:
			cloud = b
		}
	}
	totalReq := local.Requests + cloud.Requests
	share := 0.0
	if totalReq > 0 {
		share = float64(cloud.Requests) / float64(totalReq)
	}
	return map[string]any{
		"since_hours":       sinceHours,
		"local":             local,
		"cloud":             cloud,
		"detail":            detail,
		"requests":          totalReq,
		"off_machine_share": share,
		"labels": map[string]string{
			"local": i18n.T("usage.egress.local"),
			"cloud": i18n.T("usage.egress.cloud"),
		},
	}, nil
}
