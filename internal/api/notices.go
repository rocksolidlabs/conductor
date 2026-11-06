package api

// WithNotices gives the server the third-party notices text it serves at
// /third-party-notices.txt in place of the embedded THIRD_PARTY_NOTICES: the
// text a main that links more modules than this one handed in
// (cli.SetNotices, which bounds it). "" keeps the embedded text, as a
// server made without the option holds it.
func WithNotices(text string) Option {
	return func(o *options) {
		if text != "" {
			o.notices = text
		}
	}
}
