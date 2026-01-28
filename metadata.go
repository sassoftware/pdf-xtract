// Copyright © 2026, SAS Institute Inc., Cary, NC, USA.  All Rights Reserved.
// SPDX-License-Identifier: BSD-3-Clause

package xtract

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"strings"

	"github.com/sassoftware/viya-pdf-xtract/logger"
)

// Meta is the unified, metadata model (Info + XMP fields).
type Meta struct {
	Title        string `json:"title,omitempty"`
	Author       string `json:"author,omitempty"`
	Subject      string `json:"subject,omitempty"`
	Keywords     string `json:"keywords,omitempty"`
	Creator      string `json:"creator,omitempty"`
	Producer     string `json:"producer,omitempty"`
	CreationDate string `json:"creationDate,omitempty"`
	ModDate      string `json:"modDate,omitempty"`
	//XMP string `json:"xmp,omitempty"`
}

// Minimal XML models to pull common XMP fields in a namespace
type xmpPacket struct {
	XMLName xml.Name `xml:"xmpmeta"`
	RDF     rdfRDF   `xml:"http://www.w3.org/1999/02/22-rdf-syntax-ns# RDF"`
}

type rdfRDF struct {
	Descriptions []rdfDescription `xml:"http://www.w3.org/1999/02/22-rdf-syntax-ns# Description"`
}

type rdfDescription struct {
	// dc:title / dc:description (rdf:Alt)
	Title       altString `xml:"http://purl.org/dc/elements/1.1/ title"`
	Description altString `xml:"http://purl.org/dc/elements/1.1/ description"`

	// dc:creator (rdf:Seq)
	Creator seqString `xml:"http://purl.org/dc/elements/1.1/ creator"`

	// pdf namespace
	PDFProducer string `xml:"http://ns.adobe.com/pdf/1.3/ Producer"`
	PDFKeywords string `xml:"http://ns.adobe.com/pdf/1.3/ Keywords"`

	// xmp namespace
	XMPCreatorTool string `xml:"http://ns.adobe.com/xap/1.0/ CreatorTool"`
	XMPCreateDate  string `xml:"http://ns.adobe.com/xap/1.0/ CreateDate"`
	XMPModifyDate  string `xml:"http://ns.adobe.com/xap/1.0/ ModifyDate"`
}

type altString struct {
	Alt struct {
		LI []string `xml:"http://www.w3.org/1999/02/22-rdf-syntax-ns# li"`
	} `xml:"http://www.w3.org/1999/02/22-rdf-syntax-ns# Alt"`
}

func (a altString) First() string {
	if len(a.Alt.LI) > 0 {
		return strings.TrimSpace(a.Alt.LI[0])
	}
	return ""
}

type seqString struct {
	Seq struct {
		LI []string `xml:"http://www.w3.org/1999/02/22-rdf-syntax-ns# li"`
	} `xml:"http://www.w3.org/1999/02/22-rdf-syntax-ns# Seq"`
}

func (s seqString) First() string {
	if len(s.Seq.LI) > 0 {
		return strings.TrimSpace(s.Seq.LI[0])
	}
	return ""
}

type xmpFields struct {
	Title, Creator, Subject, Keywords, CreatorTool, Producer, CreateDate, ModifyDate string
}

// internal accessPerm representation
type accessPerm struct {
	canPrint             bool
	canModify            bool
	extractContent       bool
	modifyAnnotations    bool
	fillInForm           bool
	extractAccessibility bool
	assembleDocument     bool
	canPrintFaithful     bool
}

type MetadataFull struct {
	// Core (Info/XMP)
	Title        string `json:"title,omitempty"`
	Author       string `json:"author,omitempty"`
	Subject      string `json:"subject,omitempty"`
	Keywords     string `json:"keywords,omitempty"`
	Creator      string `json:"creator,omitempty"`
	Producer     string `json:"producer,omitempty"`
	CreationDate string `json:"creationDate,omitempty"`
	ModDate      string `json:"modDate,omitempty"`

	// Structural
	PDFVersion              string `json:"pdf:PDFVersion,omitempty"`
	HasXMP                  bool   `json:"pdf:hasXMP"`
	HasCollection           bool   `json:"pdf:hasCollection"`
	Encrypted               bool   `json:"pdf:encrypted"`
	NPages                  int    `json:"xmpTPg:NPages,omitempty"`
	ContainsNonEmbeddedFont bool   `json:"pdf:containsNonEmbeddedFont"`
	Language                string `json:"language,omitempty"`

	// Access permissions (Standard Security)
	AccessPermission AccessPermission `json:"access_permission"`
}

// ---- access permissions (Standard Security) --------------------------------
// Based on ISO 32000-1 §7.6.3 / Adobe PDF spec:
// P bits (least significant bit is 1):
// bit 3 (1<<2): print
// bit 4 (1<<3): modify
// bit 5 (1<<4): extract
// bit 6 (1<<5): annotate / fill forms
// bit 9 (1<<8): fill forms (older revs fold with annotate)
// bit 10 (1<<9): extract for accessibility
// bit 11 (1<<10): assemble
// bit 12 (1<<11): print high quality
// In Standard Security a bit set to 1 means the permission is granted.
// We'll compute booleans conservatively.

type AccessPermission struct {
	CanPrint                bool `json:"can_print"`
	CanPrintFaithful        bool `json:"can_print_faithful"`
	CanModify               bool `json:"can_modify"`
	ExtractContent          bool `json:"extract_content"`
	ModifyAnnotations       bool `json:"modify_annotations"`
	FillInForm              bool `json:"fill_in_form"`
	ExtractForAccessibility bool `json:"extract_for_accessibility"`
	AssembleDocument        bool `json:"assemble_document"`
}

// prefer returns a if non-empty after trimming, otherwise b.
func prefer(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// InfoDict returns the raw /Info dictionary as a Value (may be Null).
func (r *Reader) InfoDict() Value {
	logger.Debug("found Info Dictionary", true)
	return r.Trailer().Key("Info")
}

// readInfo extracts metadata stored in the PDF's /Info dictionary.
func (r *Reader) readInfo() Meta {
	logger.Debug("reading Info dictionary")
	info := r.InfoDict()
	return Meta{
		Title:        info.Key("Title").Text(),
		Author:       info.Key("Author").Text(),
		Subject:      info.Key("Subject").Text(),
		Keywords:     info.Key("Keywords").Text(),
		Creator:      info.Key("Creator").Text(),
		Producer:     info.Key("Producer").Text(),
		CreationDate: info.Key("CreationDate").Text(),
		ModDate:      info.Key("ModDate").Text(),
	}
}

// readXMP returns the raw XMP XML from /Root/Metadata (empty string if absent).
func (r *Reader) readXMP() (string, error) {
	logger.Debug("reading XMP Stream")
	md := r.Trailer().Key("Root").Key("Metadata")
	if md.Kind() != Stream {
		logger.Debug("readXMP: no XMP stream present")
		return "", nil
	}
	logger.Debug("found XMP Stream", true)
	rc := md.Reader()
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		logger.Error("readXMP: failed to read XMP stream")
		return "", err
	}
	return string(b), nil
}

// parseXMPWithXML tries to parse XMP XML using encoding/xml into xmpPacket.
func parseXMPWithXML(x string) (xmpFields, bool) {
	logger.Debug("parsing XMP")
	var pkt xmpPacket
	dec := xml.NewDecoder(strings.NewReader(x))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity

	if err := dec.Decode(&pkt); err != nil {
		return xmpFields{}, false
	}

	var f xmpFields
	for _, d := range pkt.RDF.Descriptions {
		if t := d.Title.First(); t != "" {
			f.Title = t
		}
		if c := d.Creator.First(); c != "" {
			f.Creator = c
		}
		if s := d.Description.First(); s != "" {
			f.Subject = s
		}
		if k := strings.TrimSpace(d.PDFKeywords); k != "" {
			f.Keywords = k
		}
		if p := strings.TrimSpace(d.PDFProducer); p != "" {
			f.Producer = p
		}
		if ct := strings.TrimSpace(d.XMPCreatorTool); ct != "" {
			f.CreatorTool = ct
		}
		if cd := strings.TrimSpace(d.XMPCreateDate); cd != "" {
			f.CreateDate = cd
		}
		if md := strings.TrimSpace(d.XMPModifyDate); md != "" {
			f.ModifyDate = md
		}
	}
	return f, true
}

// parseXMPFallback performs a simple tag-search fallback if XML parsing fails.
func parseXMPFallback(xmp string) xmpFields {
	logger.Debug("perform a simple tag-search fallback if XML parsing fails")
	get := func(cands ...string) string {
		for _, t := range cands {
			open, close := "<"+t+">", "</"+t+">"
			if i := strings.Index(xmp, open); i >= 0 {
				if j := strings.Index(xmp[i+len(open):], close); j >= 0 {
					return strings.TrimSpace(stripXMLTags(xmp[i+len(open) : i+len(open)+j]))
				}
			}
		}
		return ""
	}
	title := get("dc:title", "pdf:Title", "xmp:Title", "rdf:li")
	creator := get("dc:creator", "pdf:Author", "xmp:Author", "rdf:li")
	return xmpFields{
		Title:       title,
		Creator:     creator,
		Subject:     get("dc:description", "pdf:Subject"),
		Keywords:    get("pdf:Keywords", "xmp:Keywords"),
		CreatorTool: get("xmp:CreatorTool"),
		Producer:    get("pdf:Producer"),
		CreateDate:  get("xmp:CreateDate"),
		ModifyDate:  get("xmp:ModifyDate"),
	}
}

// stripXMLTags removes simple XML tags from a string.
func stripXMLTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// Metadata returns unified metadata with XMP taking precedence over /Info.
func (r *Reader) Metadata() (Meta, error) {
	info := r.readInfo()

	xmpXML, err := r.readXMP()
	if err != nil {
		return Meta{}, err
	}

	var xf xmpFields
	if xmpXML != "" {
		if got, ok := parseXMPWithXML(xmpXML); ok {
			xf = got
		} else {
			xf = parseXMPFallback(xmpXML)
		}
	}

	return Meta{
		Title:        prefer(xf.Title, info.Title),
		Author:       prefer(xf.Creator, info.Author),
		Subject:      prefer(xf.Subject, info.Subject),
		Keywords:     prefer(xf.Keywords, info.Keywords),
		Creator:      prefer(xf.CreatorTool, info.Creator),
		Producer:     prefer(xf.Producer, info.Producer),
		CreationDate: prefer(xf.CreateDate, info.CreationDate),
		ModDate:      prefer(xf.ModifyDate, info.ModDate),
		//XMP:          xmpXML,
	}, nil
}

// MetadataJSON writes the full metadata as pretty JSON to the provided writer.
func (r *Reader) MetadataJSON(w io.Writer) error {
	mf, err := r.MetadataFull()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(mf)
}

// headerVersion returns the PDF header version string.
func (r *Reader) headerVersion() string {
	buf := make([]byte, 64)
	n, _ := r.f.ReadAt(buf, 0)
	line := string(buf[:n])
	i := strings.Index(line, "%PDF-")
	if i < 0 {
		return ""
	}
	line = line[i:]
	j := strings.IndexAny(line, "\r\n")
	if j >= 0 {
		line = line[:j]
	}
	if strings.HasPrefix(line, "%PDF-") {
		return strings.TrimPrefix(line, "%PDF-")
	}
	return ""
}

// hasXMP reports whether the PDF has an XMP metadata stream.
func (r *Reader) hasXMP() bool {
	md := r.Trailer().Key("Root").Key("Metadata")
	return md.Kind() == Stream
}

// hasCollection reports whether the PDF contains a Collection dictionary.
func (r *Reader) hasCollection() bool {
	return !r.Trailer().Key("Root").Key("Collection").IsNull()
}

// isEncrypted reports whether the PDF file is encrypted.
func (r *Reader) isEncrypted() bool {
	return r.Trailer().Key("Encrypt").Kind() == Dict
}

// accessPermissions computes the effective access permissions from Encrypt.P.
func (r *Reader) accessPermissions() accessPerm {
	var ap accessPerm
	enc := r.Trailer().Key("Encrypt")
	if enc.Kind() == Null {
		// no encryption => all allowed
		return accessPerm{
			canPrint:             true,
			canModify:            true,
			extractContent:       true,
			modifyAnnotations:    true,
			fillInForm:           true,
			extractAccessibility: true,
			assembleDocument:     true,
			canPrintFaithful:     true,
		}
	}
	p := uint32(enc.Key("P").Int64())
	ap.canPrint = (p & (1 << 2)) != 0
	ap.canModify = (p & (1 << 3)) != 0
	ap.extractContent = (p & (1 << 4)) != 0
	ap.modifyAnnotations = (p & (1 << 5)) != 0
	ap.fillInForm = (p&(1<<8)) != 0 || ap.modifyAnnotations
	ap.extractAccessibility = (p & (1 << 9)) != 0
	ap.assembleDocument = (p & (1 << 10)) != 0
	ap.canPrintFaithful = (p&(1<<11)) != 0 || ap.canPrint
	return ap
}

// containsNonEmbeddedFont returns true if any page references a non-embedded font.
func (r *Reader) containsNonEmbeddedFont() bool {
	pages := r.NumPage()
	for i := 1; i <= pages; i++ {
		p := r.Page(i)
		fd := p.Resources().Key("Font")
		if fd.Kind() != Dict {
			continue
		}
		for _, fname := range fd.Keys() {
			f := p.Font(fname)
			desc := f.V.Key("FontDescriptor")
			if desc.Kind() != Dict {
				// no descriptor => not embedded
				return true
			}
			if desc.Key("FontFile").Kind() == Stream ||
				desc.Key("FontFile2").Kind() == Stream ||
				desc.Key("FontFile3").Kind() == Stream {
				// embedded
				continue
			}
			return true
		}
	}
	return false
}

// MetadataFull returns a comprehensive metadata report for the PDF.
func (r *Reader) MetadataFull() (MetadataFull, error) {
	logger.Debug("metadata extracted", true)
	var out MetadataFull

	md, err := r.Metadata()
	if err != nil {
		return out, err
	}
	out.Title = md.Title
	out.Author = md.Author
	out.Subject = md.Subject
	out.Keywords = md.Keywords
	out.Creator = md.Creator
	out.Producer = md.Producer
	out.CreationDate = md.CreationDate
	out.ModDate = md.ModDate

	out.PDFVersion = strings.TrimSpace(r.headerVersion())
	out.HasXMP = r.hasXMP()
	out.HasCollection = r.hasCollection()
	out.Encrypted = r.isEncrypted()
	out.NPages = r.NumPage()
	out.ContainsNonEmbeddedFont = r.containsNonEmbeddedFont()

	// Access permissions
	ap := r.accessPermissions()
	out.AccessPermission = AccessPermission{
		CanPrint:                ap.canPrint,
		CanPrintFaithful:        ap.canPrintFaithful,
		CanModify:               ap.canModify,
		ExtractContent:          ap.extractContent,
		ModifyAnnotations:       ap.modifyAnnotations,
		FillInForm:              ap.fillInForm,
		ExtractForAccessibility: ap.extractAccessibility,
		AssembleDocument:        ap.assembleDocument,
	}

	return out, nil
}
