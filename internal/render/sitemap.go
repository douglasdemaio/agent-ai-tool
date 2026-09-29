package render

import (
	"encoding/xml"
	"strings"
	"time"
)

type urlSet struct {
	XMLName xml.Name     `xml:"urlset"`
	Xmlns   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

func (s Site) sitemap(views []entryView) []byte {
	set := urlSet{
		Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9",
		URLs:  make([]sitemapURL, 0, len(views)+1),
	}
	// The index lastmod is the newest entry last_verified, not the build time.
	// The site is rebuilt for reasons that do not change the directory (a live
	// snapshot refresh, a metrics cache hit), and a lastmod that moves on every
	// rebuild tells crawlers the listing changes when it did not.
	var newest time.Time
	for _, v := range views {
		if v.Entry.LastVerified.After(newest) {
			newest = v.Entry.LastVerified
		}
	}
	set.URLs = append(set.URLs, sitemapURL{
		Loc:     s.canonical(""),
		LastMod: newest.UTC().Format(time.RFC3339),
	})
	for _, v := range views {
		set.URLs = append(set.URLs, sitemapURL{
			Loc:     s.canonical(v.Entry.Slug),
			LastMod: v.Entry.LastVerified.UTC().Format(time.RFC3339),
		})
	}
	body, err := xml.MarshalIndent(set, "", "  ")
	if err != nil {
		// The URL set contains only strings and times, so this cannot fail in
		// practice; emit a minimal valid document rather than nothing.
		return []byte(xml.Header + `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"></urlset>`)
	}
	var b strings.Builder
	b.WriteString(xml.Header)
	b.Write(body)
	b.WriteString("\n")
	return []byte(b.String())
}
