// Package fracdex implements fractional indexing: generating short, ordered string keys
// such that a new key can always be produced strictly between any two existing keys,
// without touching any other key.
//
// # Why this exists
//
// Junto needs to reorder items in a list while other users are concurrently editing them.
// The candidate representations for "order":
//
//   - Integer positions. A single reorder rewrites O(N) rows, which becomes O(N) operations
//     in the sync log and an conflict surface that grows with list length. Rejected.
//   - Float or numeric midpoints. Elegant until roughly 50 repeated inserts at the same
//     slot exhaust double precision — a reachable, silent failure. Rejected.
//   - Linked list (prev_id / next_id). O(N) reads to render a list, and concurrent moves
//     can produce cycles. Rejected.
//   - Fractional index strings. A move is exactly one row and one operation. Chosen.
//
// Keys are compared lexicographically by byte. The database columns are declared
// COLLATE "C" so Postgres ordering is bit-identical to Go's string comparison regardless
// of the database locale.
//
// # Relationship to the sync engine
//
// This choice is deliberately neutral between OT and CRDT — it does not pre-decide the
// Stage 2 design. It only rules out the representations that work for neither.
//
// Concurrent inserts into the same slot can legitimately generate the *same* key on two
// clients. That is expected and handled by ordering on (position, id): the id tiebreak is
// deterministic, so every replica derives the same total order from the same set of rows.
//
// # Accepted trade-off
//
// Keys grow in length under pathological repeated insertion at the same position (each
// insert between two adjacent keys adds roughly one character). A periodic rebalance pass
// can compact them; that is not implemented, and the 128-character CHECK constraint in the
// schema is the backstop that turns unbounded growth into a loud error rather than silent
// corruption.
//
// # Attribution
//
// The algorithm follows David Greenspan's "Implementing Fractional Indexing" and the
// reference `fractional-indexing` implementation. The integer-part encoding keeps keys
// short for the common append-to-end case instead of growing one character per append.
package fracdex

import (
	"errors"
	"fmt"
	"strings"
)

// Digits is the base-62 alphabet, ordered so that ASCII byte order matches digit value:
// '0' < '9' < 'A' < 'Z' < 'a' < 'z'. This is what lets plain lexicographic string
// comparison act as numeric comparison.
const Digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// smallestInteger is the reserved lower bound of the integer-part space. A key equal to it
// is invalid: it is the sentinel that signals "cannot decrement any further".
var smallestInteger = "A" + strings.Repeat(string(Digits[0]), 26)

var (
	// ErrInvalidKey means a supplied key is not a well-formed order key.
	ErrInvalidKey = errors.New("fracdex: invalid order key")
	// ErrOutOfOrder means the caller passed bounds where a >= b.
	ErrOutOfOrder = errors.New("fracdex: lower bound is not less than upper bound")
	// ErrExhausted means the integer-part space ran out. Reaching this requires ~62^26
	// appends in one direction; it exists so the failure is explicit rather than silent.
	ErrExhausted = errors.New("fracdex: order key space exhausted")
)

// First returns the key to use for the first element of an empty list.
func First() string { return "a" + string(Digits[0]) }

// KeyBetween returns a key that sorts strictly between a and b.
//
// An empty string means "unbounded": KeyBetween("", b) produces a key before b,
// KeyBetween(a, "") produces a key after a, and KeyBetween("", "") produces the first key.
// Empty is never itself a valid key, so this overload is unambiguous.
func KeyBetween(a, b string) (string, error) {
	if a != "" {
		if err := validateOrderKey(a); err != nil {
			return "", err
		}
	}
	if b != "" {
		if err := validateOrderKey(b); err != nil {
			return "", err
		}
	}
	if a != "" && b != "" && a >= b {
		return "", fmt.Errorf("%w: %q >= %q", ErrOutOfOrder, a, b)
	}

	switch {
	case a == "" && b == "":
		return First(), nil

	case a == "":
		ib, err := integerPart(b)
		if err != nil {
			return "", err
		}
		fb := b[len(ib):]
		if ib == smallestInteger {
			// Already at the floor of the integer space; go deeper into the fraction.
			m, err := midpoint("", fb)
			if err != nil {
				return "", err
			}
			return ib + m, nil
		}
		if ib < b {
			// b has a fractional part, so its own integer part already sorts before it.
			return ib, nil
		}
		return decrementInteger(ib)

	case b == "":
		ia, err := integerPart(a)
		if err != nil {
			return "", err
		}
		i, err := incrementInteger(ia)
		if errors.Is(err, ErrExhausted) {
			// Top of the integer space: extend the fraction instead of the integer.
			m, mErr := midpoint(a[len(ia):], "")
			if mErr != nil {
				return "", mErr
			}
			return ia + m, nil
		}
		if err != nil {
			return "", err
		}
		return i, nil

	default:
		ia, err := integerPart(a)
		if err != nil {
			return "", err
		}
		ib, err := integerPart(b)
		if err != nil {
			return "", err
		}
		fa, fb := a[len(ia):], b[len(ib):]
		if ia == ib {
			m, err := midpoint(fa, fb)
			if err != nil {
				return "", err
			}
			return ia + m, nil
		}
		i, err := incrementInteger(ia)
		if err != nil {
			return "", err
		}
		if i < b {
			return i, nil
		}
		m, err := midpoint(fa, "")
		if err != nil {
			return "", err
		}
		return ia + m, nil
	}
}

// NKeysBetween returns n keys sorting strictly between a and b, in ascending order.
//
// It bisects rather than chaining, so inserting n items between two neighbours yields keys
// of O(log n) length instead of O(n). Used for bulk operations like seeding a trip's days.
func NKeysBetween(a, b string, n int) ([]string, error) {
	if n < 0 {
		return nil, fmt.Errorf("fracdex: negative count %d", n)
	}
	if n == 0 {
		return []string{}, nil
	}
	if n == 1 {
		k, err := KeyBetween(a, b)
		if err != nil {
			return nil, err
		}
		return []string{k}, nil
	}

	if b == "" {
		c, err := KeyBetween(a, b)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, n)
		out = append(out, c)
		for i := 0; i < n-1; i++ {
			if c, err = KeyBetween(c, b); err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, nil
	}

	if a == "" {
		c, err := KeyBetween(a, b)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, n)
		out = append(out, c)
		for i := 0; i < n-1; i++ {
			if c, err = KeyBetween(a, c); err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		// Built descending; reverse to ascending.
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
		return out, nil
	}

	mid := n / 2
	c, err := KeyBetween(a, b)
	if err != nil {
		return nil, err
	}
	left, err := NKeysBetween(a, c, mid)
	if err != nil {
		return nil, err
	}
	right, err := NKeysBetween(c, b, n-mid-1)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, n)
	out = append(out, left...)
	out = append(out, c)
	out = append(out, right...)
	return out, nil
}

// midpoint returns a fraction string strictly between fractions a and b, where both are
// interpreted as digits following an implicit "0.". An empty b means 1 (no upper bound).
//
// Empty b is unambiguous here: a genuinely empty upper fraction can never reach this
// function, because it would imply a >= b at the caller.
func midpoint(a, b string) (string, error) {
	if b != "" && a >= b {
		return "", fmt.Errorf("%w: %q >= %q", ErrOutOfOrder, a, b)
	}
	zero := Digits[0]
	if (len(a) > 0 && a[len(a)-1] == zero) || (len(b) > 0 && b[len(b)-1] == zero) {
		// Trailing zeros would make the representation non-canonical: "1" and "10" would
		// denote the same fraction, and midpoint's recursion would not terminate cleanly.
		return "", fmt.Errorf("%w: trailing zero in fraction", ErrInvalidKey)
	}

	if b != "" {
		// Strip the longest common prefix, padding a with zeros as needed, then recurse
		// on the remainder. b never runs out first: b has no trailing zero, so the loop
		// always breaks at or before its last digit.
		n := 0
		for n < len(b) {
			ca := zero
			if n < len(a) {
				ca = a[n]
			}
			if ca != b[n] {
				break
			}
			n++
		}
		if n > 0 {
			var restA string
			if n < len(a) {
				restA = a[n:]
			}
			rest, err := midpoint(restA, b[n:])
			if err != nil {
				return "", err
			}
			return b[:n] + rest, nil
		}
	}

	// The leading digits differ (or a is empty).
	digitA := 0
	if a != "" {
		digitA = digitIndex(a[0])
		if digitA < 0 {
			return "", fmt.Errorf("%w: bad digit %q", ErrInvalidKey, a[0])
		}
	}
	digitB := len(Digits)
	if b != "" {
		digitB = digitIndex(b[0])
		if digitB < 0 {
			return "", fmt.Errorf("%w: bad digit %q", ErrInvalidKey, b[0])
		}
	}

	if digitB-digitA > 1 {
		// There is room for a digit strictly between them.
		return string(Digits[(digitA+digitB+1)/2]), nil
	}
	// Leading digits are consecutive: no room at this depth.
	if len(b) > 1 {
		return b[:1], nil
	}
	// b is unbounded or a single digit: keep a's leading digit and go one level deeper.
	var restA string
	if len(a) > 1 {
		restA = a[1:]
	}
	rest, err := midpoint(restA, "")
	if err != nil {
		return "", err
	}
	return string(Digits[digitA]) + rest, nil
}

// integerLength decodes the magnitude header. Positive magnitudes use 'a'..'z' (lengths
// 2..27) and negative ones 'Z'..'A' (lengths 2..27), so that byte order still matches
// numeric order across the sign boundary.
func integerLength(head byte) (int, error) {
	switch {
	case head >= 'a' && head <= 'z':
		return int(head-'a') + 2, nil
	case head >= 'A' && head <= 'Z':
		return int('Z'-head) + 2, nil
	default:
		return 0, fmt.Errorf("%w: bad integer head %q", ErrInvalidKey, head)
	}
}

func integerPart(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidKey)
	}
	n, err := integerLength(key[0])
	if err != nil {
		return "", err
	}
	if n > len(key) {
		return "", fmt.Errorf("%w: %q is shorter than its declared integer length", ErrInvalidKey, key)
	}
	return key[:n], nil
}

func validateInteger(i string) error {
	n, err := integerLength(i[0])
	if err != nil {
		return err
	}
	if len(i) != n {
		return fmt.Errorf("%w: integer part %q has wrong length", ErrInvalidKey, i)
	}
	for j := 1; j < len(i); j++ {
		if digitIndex(i[j]) < 0 {
			return fmt.Errorf("%w: bad digit %q in integer part", ErrInvalidKey, i[j])
		}
	}
	return nil
}

func validateOrderKey(key string) error {
	if key == smallestInteger {
		return fmt.Errorf("%w: %q is the reserved lower bound", ErrInvalidKey, key)
	}
	i, err := integerPart(key)
	if err != nil {
		return err
	}
	if err := validateInteger(i); err != nil {
		return err
	}
	f := key[len(i):]
	if len(f) > 0 && f[len(f)-1] == Digits[0] {
		return fmt.Errorf("%w: %q has a trailing zero", ErrInvalidKey, key)
	}
	for j := 0; j < len(f); j++ {
		if digitIndex(f[j]) < 0 {
			return fmt.Errorf("%w: bad digit %q in fraction", ErrInvalidKey, f[j])
		}
	}
	return nil
}

func incrementInteger(x string) (string, error) {
	if err := validateInteger(x); err != nil {
		return "", err
	}
	head := x[0]
	digs := []byte(x[1:])
	carry := true
	for i := len(digs) - 1; carry && i >= 0; i-- {
		d := digitIndex(digs[i]) + 1
		if d == len(Digits) {
			digs[i] = Digits[0]
		} else {
			digs[i] = Digits[d]
			carry = false
		}
	}
	if !carry {
		return string(head) + string(digs), nil
	}
	// Overflowed this magnitude; move to the next header.
	if head == 'Z' {
		return "a" + string(Digits[0]), nil
	}
	if head == 'z' {
		return "", ErrExhausted
	}
	h := head + 1
	if h > 'a' {
		digs = append(digs, Digits[0])
	} else {
		digs = digs[:len(digs)-1]
	}
	return string(h) + string(digs), nil
}

func decrementInteger(x string) (string, error) {
	if err := validateInteger(x); err != nil {
		return "", err
	}
	head := x[0]
	digs := []byte(x[1:])
	last := Digits[len(Digits)-1]
	borrow := true
	for i := len(digs) - 1; borrow && i >= 0; i-- {
		d := digitIndex(digs[i]) - 1
		if d < 0 {
			digs[i] = last
		} else {
			digs[i] = Digits[d]
			borrow = false
		}
	}
	if !borrow {
		return string(head) + string(digs), nil
	}
	if head == 'a' {
		return "Z" + string(last), nil
	}
	if head == 'A' {
		return "", ErrExhausted
	}
	h := head - 1
	if h < 'Z' {
		digs = append(digs, last)
	} else {
		digs = digs[:len(digs)-1]
	}
	return string(h) + string(digs), nil
}

func digitIndex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'Z':
		return int(c-'A') + 10
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 36
	default:
		return -1
	}
}
