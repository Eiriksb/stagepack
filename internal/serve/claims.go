package serve

import (
	"errors"
	"log"

	"stagepack/internal/instance"
)

// claim is the claim on the deck in one home.
type claim struct {
	home string
	*instance.Handle
}

// claims are one process's claims on one deck: the first in the program's own home, the others
// in the peer homes. The editor and the plain Stagepack are installed in different folders by
// design, and each looks only in its own; two copies on one file would each overwrite the other's
// saves. A record in a peer home is the normal record, with this process's PID and address, so a
// Stagepack that knows nothing of editors finds it and hands over to this server.
//
// A peer home is a courtesy to the other program and can't stop the talk from opening: one that
// can't be written is skipped with a line in the log. The own home's errors count.
type claims []claim

// claimDeck claims deckPath in home, then in each peer home. If an instance is alive in any of
// them it returns that record and no claims, having given up those already made.
func claimDeck(home string, peers []string, deckPath string, alive func(instance.Record) bool) (*instance.Record, claims, error) {
	existing, h, err := instance.Claim(home, deckPath, alive)
	if err != nil || existing != nil {
		return existing, nil, err
	}
	c := claims{{home, h}}
	for _, peer := range peers {
		existing, h, err := instance.Claim(peer, deckPath, alive)
		switch {
		case err != nil:
			log.Printf("peer home %s can't be written, so it is skipped: %v", peer, err)
		case existing != nil:
			c.release()
			return existing, nil, nil
		default:
			c = append(c, claim{peer, h})
		}
	}
	return nil, c, nil
}

// each runs op on every claim and joins what counts of the errors: all of the own home's, and
// from a peer home only that another launch has replaced this one's record (ErrNotOwner with a
// Holder). A record that is simply gone from a peer home, or can't be written, is skipped.
func (c claims) each(what string, op func(*instance.Handle) error) error {
	var errs []error
	for i, cl := range c {
		err := op(cl.Handle)
		switch {
		case err == nil:
		case i == 0 || errors.Is(err, instance.ErrNotOwner) && cl.Holder() != nil:
			errs = append(errs, err)
		default:
			log.Printf("%s in peer home %s: %v; skipped", what, cl.home, err)
		}
	}
	return errors.Join(errs...)
}

// update records where Slidev is listening and the work folder, in every home (Handle.Update).
func (c claims) update(port int, baseURL, workDir string) error {
	return c.each("record the running instance", func(h *instance.Handle) error { return h.Update(port, baseURL, workDir) })
}

// renew starts every claim's "still starting" time again (Handle.Renew).
func (c claims) renew() error {
	return c.each("renew the claim", func(h *instance.Handle) error { return h.Renew() })
}

// release gives up every claim that is still this process's. Safe to call more than once.
func (c claims) release() {
	for _, cl := range c {
		cl.Release()
	}
}

// holderBaseURL is the address of the launch that took the deck over, in the first home where
// one did; empty if none did.
func (c claims) holderBaseURL() string {
	for _, cl := range c {
		if r := cl.Holder(); r != nil {
			return r.BaseURL
		}
	}
	return ""
}
