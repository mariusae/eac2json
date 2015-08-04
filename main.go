// Eac2json parses Schwab's Employee Awards Center transaction
// history page from HTML into JSON. The output is a sequence of
// entries, each of which is a key-value bag. They are in the same
// order as they appear in the input document. Eac2json coalesces
// "more details" entries into its parent entry; their key-values
// appear together as one.
//
// In order to use eac2json, you must first save the HTML from the
// Schwab Employee Awards Center history page. Go to  the "My Equity
// Awards" tab, then to the "History & Statements" sub-tab. Set the
// date range to "All", and save the page to a file. Chrome doesn't
// seem to do very well; Safari works fine.
//
// NB! Eac2json is intended to assist in computing wash sales only.
// It ignores certain entries that are not relevant for these purposes.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"golang.org/x/net/html"
)

type Node struct {
	*html.Node
	next *html.Node
	err  error

	stack *Node
}

func (n *Node) Sibling(tag string) {
	if n.err != nil {
		return
	}

	for c := n.next; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == tag {
			n.Node = c
			n.next = c.NextSibling
			return
		}
	}

	n.err = errors.New(fmt.Sprintf("no sibling tag %s", tag))
}

func (n *Node) Child(tag string) {
	if n.err != nil {
		return
	}

	n.Node = n.FirstChild
	n.next = n.Node
	n.Sibling(tag)
}

// Root out the next (child) text node without advancing the cursor.
func (n *Node) Text() string {
	if n.err != nil {
		return ""
	}

	for c := n.Node; c != nil; c = c.FirstChild {
		if c.Type == html.TextNode {
			return c.Data
		}
	}

	return ""
}

func (n *Node) Push() {
	m := new(Node)
	*m = *n
	n.stack = m
}

func (n *Node) Pop() {
	*n = *n.stack
}

func (n *Node) Error() error {
	return n.err
}

func (n *Node) Ok() bool {
	return n.err == nil
}

type Ledger struct {
	entries []map[string]string
	e       map[string]string
}

func (l *Ledger) Next() {
	if len(l.e) > 0 {
		l.entries = append(l.entries, l.e)
	}
	l.e = make(map[string]string)
}

func (l *Ledger) Write(k, v string) {
	l.e[k] = v
}

func findHistory(n *html.Node) *html.Node {
	if n.Type == html.ElementNode && n.Data == "a" {
		for _, a := range n.Attr {
			if a.Key == "name" && a.Val == "History" {
				return n
			}
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		n := findHistory(c)
		if n != nil {
			return n
		}
	}

	return nil
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: eac2json eac.html\n")
	os.Exit(2)
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("eac2json: ")

	if len(os.Args) != 2 {
		usage()
	}

	file, err := os.Open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}

	doc, err := html.Parse(file)
	if err != nil {
		log.Fatal(err)
	}

	// First find the transaction history table.
	root := findHistory(doc)
	if root == nil {
		log.Fatalf("no history")
	}

	// Now grub out the actual table body.
	n := &Node{root, root, nil, nil}
	n.Child("table")
	n.Child("tbody")
	n.Child("tr")
	n.Sibling("tr")
	n.Child("td")
	n.Child("table")
	n.Child("tbody")

	if !n.Ok() {
		log.Fatal(n.Error())
	}

	if n.Type != html.ElementNode || n.Data != "tbody" {
		log.Fatalf("bad table node %v type %d data %s", n, n.Type, n.Data)
	}

	n.Child("tr")

	// The first row is the header
	ok, header := row(n)
	if !ok {
		log.Fatal("Found no header")
	}

	action := -1
	for i := range header {
		if header[i] == "Action" {
			action = i
		}
	}
	if action < 0 {
		log.Fatal("\"Action\" header not found")
	}

	l := &Ledger{}

	for n.Sibling("tr"); n.Ok(); n.Sibling("tr") {
		// First try to extract a regular data row.
		ok, values := row(n)
		if !ok {
			log.Fatal("Unrecognized row")
		}

		// Schwab's transaction history generates duplicate entries
		// for many actions, so it's important to be careful here
		// to account for each action only once. Further, we only
		// extract acquisitions (purchases) and sales (e.g., which may
		// be done automatically for tax purposes).
		//
		// - "Lapse" entries are summary entries; they are covered by
		// "Deposit" and "Forced Quick Sell" entries.
		switch values[action] {
		case "Deposit", "Forced Quick Sell", "Exer and Hold":
			// - Deposits: deposits RSUs into your EAC account;
			// - Forced Quick Sell: immediate sale of RSUs for taxes;
			// - Exer and Hold: exercise and hold.

			// Incorporate data into the row.
			l.Next()

			for i := range header {
				l.Write(header[i], values[i])
			}

			n.Sibling("tr")
			if err := more(n, l); err != nil {
				log.Fatal(err)
			}

		case "Journal":
			// The next row holds more details, but it's not useful to us.
			n.Sibling("tr")

		case "Lapse":
			// These are summary entries for RSU lapses.
			// They are covered by deposits & forced quick sells.
			n.Sibling("tr")

		case "Sale":
			// NB!! We ignore options and ESPP sales as these should never be
			// at a loss, and thus unimportant to computing wash sales.
			// (This is unlike Forced Quick Sells, which may be at a loss.)
			n.Sibling("tr")

		case "Forced Disbursement":
			// Not relevant.

		default:
			log.Fatalf(fmt.Sprintf("unknown row type \"%s\"", action))
		}
	}

	w := bufio.NewWriter(os.Stdout)
	enc := json.NewEncoder(w)
	if err := enc.Encode(l.entries); err != nil {
		log.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		log.Fatal(err)
	}
}

// Extract a regular data row.
func row(n *Node) (bool, []string) {
	n.Push()
	defer n.Pop()

	var values []string
	for n.Child("td"); n.Ok(); n.Sibling("td") {
		n.Push()
		n.Child("label")
		val := strings.TrimSpace(n.Text())
		if !n.Ok() {
			n.Pop()
			return false, nil
		}

		n.Pop()
		values = append(values, val)
	}

	return true, values
}

// Extract a standard "more details" row.
func more(n *Node, l *Ledger) error {
	n.Push()
	defer n.Pop()

	n.Child("td")
	n.Child("div")
	n.Child("div")
	n.Child("table")
	n.Sibling("table")
	n.Child("tbody")
	if n.Error() != nil {
		return n.Error()
	}

	// First row is headers.
	n.Child("tr")

	var headers []string

	n.Push()
	for n.Child("td"); n.Ok(); n.Sibling("td") {
		n.Push()
		n.Child("b")
		headers = append(headers, strings.TrimSpace(n.Text()))
		n.Pop()
	}
	n.Pop()

	i := 0
	n.Sibling("tr")
	for n.Child("td"); n.Ok(); n.Sibling("td") {
		l.Write(headers[i], strings.TrimSpace(n.Text()))
		i++
	}

	return nil
}
