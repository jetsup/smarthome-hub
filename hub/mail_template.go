package hub

import (
	"fmt"
	"html"
	"os"
	"strings"
)

// CodeEmail is the content of a verification-code email. One template serves every
// message that carries a code (sign-in, turning on email verification, ...).
type CodeEmail struct {
	Subject   string
	Preheader string
	Heading   string
	Intro     string
	Code      string
	Minutes   int
	Details   [][2]string // "Requested from" → "Chrome on Linux", ...
	Notice    string      // the "if this wasn't you" line
}

// renderCodeEmail returns the HTML and plain-text bodies. The HTML is table-based
// with inline styles so it renders in Gmail, Outlook and Apple Mail, and it adapts to
// dark mode and small screens.
func renderCodeEmail(d CodeEmail) (htmlOut, textOut string) {
	e := html.EscapeString
	appURL := strings.TrimRight(os.Getenv("PUBLIC_URL"), "/")

	// One tile per digit reads far better than a single run of numbers.
	var digits strings.Builder
	for _, r := range d.Code {
		fmt.Fprintf(&digits, `<td class="digit" style="padding:0 4px;"><div class="digit-box" style="width:44px;height:56px;line-height:56px;text-align:center;font-family:'SF Mono','JetBrains Mono',Menlo,Consolas,monospace;font-size:28px;font-weight:700;color:#0b1220;background:#eef4ff;border:1px solid #cfe0ff;border-radius:12px;">%s</div></td>`, e(string(r)))
	}

	var details strings.Builder
	for _, kv := range d.Details {
		fmt.Fprintf(&details, `<tr><td class="muted" style="padding:6px 0;color:#6b7684;font-size:13px;width:38%%;">%s</td><td class="text" style="padding:6px 0;color:#1f2933;font-size:13px;font-weight:600;">%s</td></tr>`, e(kv[0]), e(kv[1]))
	}

	button := ""
	if appURL != "" {
		button = fmt.Sprintf(`<tr><td align="center" style="padding:6px 32px 28px;"><a href="%s" style="display:inline-block;background:#2f6feb;color:#ffffff;text-decoration:none;font-weight:600;font-size:14px;padding:12px 26px;border-radius:10px;">Open SmartHome</a></td></tr>`, e(appURL))
	}

	htmlOut = fmt.Sprintf(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark"><meta name="supported-color-schemes" content="light dark">
<title>%[1]s</title>
<style>
  @media (prefers-color-scheme: dark) {
    .page { background:#070b14 !important; }
    .card { background:#111827 !important; border-color:#1f2a3d !important; }
    .text { color:#e6edf6 !important; }
    .muted { color:#93a1b5 !important; }
    .digit-box { background:#0f1e3a !important; border-color:#1f3a6b !important; color:#9ec5ff !important; }
    .notice { background:#2a1b1b !important; border-color:#5a2a2a !important; color:#f2b8b5 !important; }
    .foot { color:#6b7a90 !important; }
  }
  @media (max-width: 480px) {
    .wrap { width:100%% !important; }
    .pad { padding-left:20px !important; padding-right:20px !important; }
    .digit { padding:0 2px !important; }
    .digit-box { width:38px !important; height:50px !important; line-height:50px !important; font-size:24px !important; }
  }
</style></head>
<body class="page" style="margin:0;padding:0;background:#eef2f8;-webkit-font-smoothing:antialiased;">
<div style="display:none;max-height:0;overflow:hidden;opacity:0;">%[2]s</div>
<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" class="page" style="background:#eef2f8;padding:32px 12px;">
<tr><td align="center">
<table role="presentation" class="wrap" width="560" cellspacing="0" cellpadding="0" style="width:560px;max-width:560px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;">

  <!-- brand header -->
  <tr><td align="center" bgcolor="#0b1220" style="background:#0b1220;background-image:linear-gradient(135deg,#0b1220 0%%,#14264d 55%%,#3b2a7a 100%%);border-radius:18px 18px 0 0;padding:30px 24px 26px;">
    <table role="presentation" cellspacing="0" cellpadding="0"><tr>
      <td bgcolor="#388bfd" style="background:#388bfd;background-image:linear-gradient(135deg,#388bfd,#a371f7);border-radius:12px;width:42px;height:42px;text-align:center;color:#fff;font-size:22px;font-weight:800;line-height:42px;">S</td>
      <td style="padding-left:12px;color:#ffffff;font-size:22px;font-weight:800;letter-spacing:-0.4px;">Smart<span style="color:#7db7ff;">Home</span></td>
    </tr></table>
    <div style="margin-top:14px;color:#9fb3d1;font-size:12px;letter-spacing:2px;text-transform:uppercase;">Security</div>
  </td></tr>

  <!-- card -->
  <tr><td class="card" style="background:#ffffff;border:1px solid #e3e9f2;border-top:0;border-radius:0 0 18px 18px;">
    <table role="presentation" width="100%%" cellspacing="0" cellpadding="0">
      <tr><td class="pad" style="padding:34px 40px 8px;">
        <h1 class="text" style="margin:0 0 10px;font-size:23px;line-height:1.3;color:#0b1220;font-weight:800;letter-spacing:-0.3px;">%[3]s</h1>
        <p class="muted" style="margin:0;font-size:15px;line-height:1.6;color:#516074;">%[4]s</p>
      </td></tr>

      <tr><td align="center" class="pad" style="padding:26px 40px 8px;">
        <table role="presentation" cellspacing="0" cellpadding="0"><tr>%[5]s</tr></table>
      </td></tr>
      <tr><td align="center" class="pad" style="padding:6px 40px 22px;">
        <span class="muted" style="font-size:13px;color:#6b7684;">This code expires in <strong class="text" style="color:#1f2933;">%[6]d minutes</strong>.</span>
      </td></tr>

      <tr><td class="pad" style="padding:0 40px 22px;">
        <table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="border-top:1px solid #e8edf5;">%[7]s</table>
      </td></tr>

      <tr><td class="pad" style="padding:0 40px 28px;">
        <div class="notice" style="background:#fff5f4;border:1px solid #fbd5d1;border-radius:12px;padding:14px 16px;font-size:13px;line-height:1.55;color:#8a2c25;">
          <strong>Didn’t request this?</strong> %[8]s
        </div>
      </td></tr>
      %[9]s
    </table>
  </td></tr>

  <!-- footer -->
  <tr><td align="center" class="foot" style="padding:22px 16px 8px;font-size:12px;line-height:1.6;color:#8a97ab;">
    Never share this code with anyone. SmartHome will never ask you for it.<br>
    You received this email because of activity on your SmartHome account.<br>
    <span style="color:#a7b2c3;">SmartHome · Centralized control for your ESP32 mesh network</span>
  </td></tr>
</table>
</td></tr></table></body></html>`,
		e(d.Subject), e(d.Preheader), e(d.Heading), e(d.Intro), digits.String(), d.Minutes, details.String(), e(d.Notice), button)

	var t strings.Builder
	fmt.Fprintf(&t, "SmartHome\n=========\n\n%s\n\n%s\n\n    %s\n\nThis code expires in %d minutes.\n\n", d.Heading, d.Intro, d.Code, d.Minutes)
	for _, kv := range d.Details {
		fmt.Fprintf(&t, "%s: %s\n", kv[0], kv[1])
	}
	fmt.Fprintf(&t, "\nDidn't request this? %s\n\nNever share this code with anyone. SmartHome will never ask you for it.\n", d.Notice)
	return htmlOut, t.String()
}
