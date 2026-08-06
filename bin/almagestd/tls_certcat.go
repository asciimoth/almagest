package main

import (
	"crypto/x509"
	"strings"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/certcat"
)

var tlsCertcatIndex = certcat.NewIndex(certcat.Records)

func tlsCertcatMatches(cert *x509.Certificate) []certcat.Record {
	return tlsCertcatIndex.MatchCertificate(cert)
}

func tlsCertcatMatchesAPI(records []certcat.Record) []api.TLSCertcatMatch {
	if len(records) == 0 {
		return nil
	}
	out := make([]api.TLSCertcatMatch, 0, len(records))
	for _, record := range records {
		match := api.TLSCertcatMatch{
			Fingerprint:    record.Fingerprint.String(),
			MatchKind:      record.MatchKind.String(),
			Kind:           record.Kind.String(),
			HazardClass:    record.HazardClass.String(),
			Action:         record.Action.String(),
			Severity:       record.Severity.String(),
			Recommendation: record.Recommendation.String(),
			Scope:          tlsCertcatScopeAPI(record.Scope),
			PlatformCutoff: tlsCertcatPlatformCutoffAPI(record.PlatformCutoff),
		}
		out = append(out, match)
	}
	return out
}

func tlsCertcatScopeAPI(scope *certcat.Scope) *api.TLSCertcatScope {
	if scope == nil {
		return nil
	}
	return &api.TLSCertcatScope{
		Basis:                     scope.Basis.String(),
		PermittedDNSSubtrees:      append([]string(nil), scope.PermittedDNSSubtrees...),
		PermittedIPCIDRs:          append([]string(nil), scope.PermittedIPCIDRs...),
		RequiresExplicitAllowlist: scope.RequiresExplicitAllowlist,
		RejectIPTargets:           scope.RejectIPTargets,
	}
}

func tlsCertcatPlatformCutoffAPI(cutoff *certcat.PlatformCutoff) *api.TLSCertcatPlatformCutoff {
	if cutoff == nil {
		return nil
	}
	return &api.TLSCertcatPlatformCutoff{
		SCTTimestampAfter: cutoff.SCTTimestampAfter,
		EnforcementStart:  cutoff.EnforcementStart,
		FirstMajorVersion: cutoff.FirstMajorVersion,
	}
}

func defaultTLSScopeForCertcat(records []certcat.Record) (api.TLSScope, bool) {
	scope := api.TLSScope{Any: true}
	for _, record := range records {
		switch record.Action {
		case certcat.ActionNarrowScope:
			next, ok := tlsScopeFromCertcatScope(record.Scope)
			if !ok {
				return api.TLSScope{}, false
			}
			scope = mergeDefaultTLSScope(scope, next)
		case certcat.ActionScopedServiceAnchor:
			if record.Scope == nil || record.Scope.RequiresExplicitAllowlist {
				return api.TLSScope{}, false
			}
			next, ok := tlsScopeFromCertcatScope(record.Scope)
			if !ok {
				return api.TLSScope{}, false
			}
			scope = mergeDefaultTLSScope(scope, next)
		case certcat.ActionDeny,
			certcat.ActionDenyEvenIfLocal,
			certcat.ActionWarn,
			certcat.ActionFollowPlatformOrDeny,
			certcat.ActionEnforcePlatformCutoff:
			return api.TLSScope{}, false
		default:
			if record.Kind == certcat.RecordKindHazardCertificate || certcatSeverityWarns(record.Severity) {
				return api.TLSScope{}, false
			}
		}
	}
	return normalizeTLSScope(scope), true
}

func tlsScopeFromCertcatScope(scope *certcat.Scope) (api.TLSScope, bool) {
	if scope == nil {
		return api.TLSScope{}, false
	}
	out := api.TLSScope{}
	for _, subtree := range scope.PermittedDNSSubtrees {
		subtree = strings.TrimSpace(subtree)
		if subtree == "" {
			continue
		}
		includeSubdomains := false
		if strings.HasPrefix(subtree, ".") {
			subtree = strings.TrimPrefix(subtree, ".")
			includeSubdomains = true
		}
		if subtree == "" {
			continue
		}
		out.DNS = append(out.DNS, api.TLSDNSRule{
			Domain:            subtree,
			IncludeSubdomains: includeSubdomains,
		})
	}
	out.IPPrefixes = append(out.IPPrefixes, scope.PermittedIPCIDRs...)
	out = normalizeTLSScope(out)
	if len(out.DNS) == 0 && len(out.IPPrefixes) == 0 {
		return api.TLSScope{}, false
	}
	if _, err := tlsScopeFromAPI(out); err != nil {
		return api.TLSScope{}, false
	}
	return out, true
}

func mergeDefaultTLSScope(left, right api.TLSScope) api.TLSScope {
	if left.Any {
		return normalizeTLSScope(right)
	}
	return mergeTLSScope(left, right)
}

func certcatSeverityWarns(severity certcat.Severity) bool {
	switch severity {
	case certcat.SeverityWarning, certcat.SeverityHigh, certcat.SeverityCritical:
		return true
	default:
		return false
	}
}
