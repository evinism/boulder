package va

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"

	"github.com/letsencrypt/boulder/core"
	"github.com/letsencrypt/boulder/probs"
)

// getAddr will query for all A/AAAA records associated with hostname and return
// the preferred address, the first net.IP in the addrs slice, and all addresses
// resolved. This is the same choice made by the Go internal resolution library
// used by net/http.
func (va ValidationAuthorityImpl) getAddrs(ctx context.Context, hostname string) ([]net.IP, *probs.ProblemDetails) {
	addrs, err := va.dnsClient.LookupHost(ctx, hostname)
	if err != nil {
		problem := probs.DNS("%v", err)
		return nil, problem
	}

	if len(addrs) == 0 {
		return nil, probs.UnknownHost("No valid IP addresses found for %s", hostname)
	}
	va.log.Debugf("Resolved addresses for %s: %s", hostname, addrs)
	return addrs, nil
}

// availableAddresses takes a ValidationRecord and splits the AddressesResolved
// into a list of IPv4 and IPv6 addresses.
func availableAddresses(allAddrs []net.IP) (v4 []net.IP, v6 []net.IP) {
	for _, addr := range allAddrs {
		if addr.To4() != nil {
			v4 = append(v4, addr)
		} else {
			v6 = append(v6, addr)
		}
	}
	return
}

func (va *ValidationAuthorityImpl) validateDNS01(ctx context.Context, identifier core.AcmeIdentifier, challenge core.Challenge) ([]core.ValidationRecord, *probs.ProblemDetails) {
	if identifier.Type != core.IdentifierDNS {
		va.log.Infof("Identifier type for DNS challenge was not DNS: %s", identifier)
		return nil, probs.Malformed("Identifier type for DNS was not itself DNS")
	}

	// Compute the digest of the key authorization file
	h := sha256.New()
	h.Write([]byte(challenge.ProvidedKeyAuthorization))
	authorizedKeysDigest := base64.RawURLEncoding.EncodeToString(h.Sum(nil))

	// Look for the required record in the DNS
	challengeSubdomain := fmt.Sprintf("%s.%s", core.DNSPrefix, identifier.Value)
	txts, authorities, err := va.dnsClient.LookupTXT(ctx, challengeSubdomain)

	if err != nil {
		va.log.Infof("Failed to lookup TXT records for %s. err=[%#v] errStr=[%s]", identifier, err, err)
		return nil, probs.DNS(err.Error())
	}

	// If there weren't any TXT records return a distinct error message to allow
	// troubleshooters to differentiate between no TXT records and
	// invalid/incorrect TXT records.
	if len(txts) == 0 {
		return nil, probs.Unauthorized("No TXT record found at %s", challengeSubdomain)
	}

	for _, element := range txts {
		if subtle.ConstantTimeCompare([]byte(element), []byte(authorizedKeysDigest)) == 1 {
			// Successful challenge validation
			return []core.ValidationRecord{{
				Authorities: authorities,
				Hostname:    identifier.Value,
			}}, nil
		}
	}

	invalidRecord := txts[0]
	if len(invalidRecord) > 100 {
		invalidRecord = invalidRecord[0:100] + "..."
	}
	var andMore string
	if len(txts) > 1 {
		andMore = fmt.Sprintf(" (and %d more)", len(txts)-1)
	}
	return nil, probs.Unauthorized("Incorrect TXT record %q%s found at %s",
		replaceInvalidUTF8([]byte(invalidRecord)), andMore, challengeSubdomain)
}
