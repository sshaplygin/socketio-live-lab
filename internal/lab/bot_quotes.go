package lab

import (
	"math/rand/v2"
	"sync"
)

// Authors are included in Docker/Moby's frozen container-name dictionary:
// https://github.com/moby/moby/blob/v27.5.1/pkg/namesgenerator/names-generator.go
// Keep the source with each short excerpt; the bot never fetches quotes at runtime.
var scientistQuotes = [...]struct{ text, author, source string }{
	{
		"We can only see a short distance ahead, but we can see plenty there that needs to be done.",
		"Alan Turing",
		"https://www.turing.ac.uk/blog/alan-turings-legacy-troubled-times",
	},
	{
		"The tools we use have a profound (and devious!) influence on our thinking habits, and, therefore, on our thinking abilities.",
		"Edsger W. Dijkstra",
		"https://www.cs.utexas.edu/~EWD/transcriptions/EWD04xx/EWD498.html",
	},
	{
		"The Analytical Engine has no pretensions whatever to originate anything.",
		"Ada Lovelace",
		"https://psychclassics.yorku.ca/Lovelace/lovelace.htm",
	},
	{
		"What I cannot create, I do not understand.",
		"Richard Feynman",
		"https://magazine.caltech.edu/post/biology-through-the-eyes-of-a-physicist",
	},
	{
		"Ignorance more frequently begets confidence than does knowledge.",
		"Charles Darwin",
		"https://www.gutenberg.org/files/2300/2300-h/2300-h.htm",
	},
	{
		"If I have seen further, it is by standing on ye shoulders of giants.",
		"Isaac Newton",
		"https://www.lindahall.org/about/news/scientist-of-the-day/isaac-newton-3/",
	},
}

// A shuffled deck avoids repeats until every quote has been used, including
// immediate repeats at cycle boundaries. Memory stays bounded to the pool size.
type quoteDeck struct {
	mu        sync.Mutex
	remaining []int
	previous  int
	started   bool
}

func (d *quoteDeck) next() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.remaining) == 0 {
		d.remaining = rand.Perm(len(scientistQuotes))
		if d.started && len(d.remaining) > 1 && d.remaining[0] == d.previous {
			d.remaining[0], d.remaining[1] = d.remaining[1], d.remaining[0]
		}
	}
	i := d.remaining[0]
	d.remaining = d.remaining[1:]
	d.previous, d.started = i, true
	quote := scientistQuotes[i]
	return "“" + quote.text + "”\n— " + quote.author
}

var chatQuotes quoteDeck
