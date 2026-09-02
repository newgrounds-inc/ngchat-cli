# Security policy

## Reporting a vulnerability

Use GitHub's private vulnerability reporting: open the **Security** tab
of this repository and choose **Report a vulnerability**. That reaches
the maintainers without a public issue. Please do not put the details
in an ordinary issue or pull request.

If that button is missing, e-mail <support@newgrounds.com> with
"ngchat security" in the subject and a maintainer will follow up from
there.

Include the version (`ngchat -version`), your OS and terminal, and the
steps to reproduce. A `-debug` log is welcome; it redacts tokens and
cookies but still contains the chat text you saw, so read it before
attaching it.

You will get an acknowledgement within five business days. Fixes ship in
the next release, and the changelog credits the reporter unless they
ask otherwise. Please give us that window before publishing details.

## Scope

This repository is the terminal client only. In scope:

- The stored credential: the site's `ng_remember` cookie in the OS
  keyring or the `0600` fallback file, and anything that could leak it
  (logs, error messages, the `-debug` log's redaction).
- The login flow and chat-token minting against the site API.
- Rendering server-supplied HTML to the terminal: escape sequences,
  OSC 8 hyperlinks, and anything that could drive the terminal rather
  than display text.
- The WebSocket protocol handling.

Newgrounds Chat itself (the server) and the site's API are separate
codebases. Reports about them are still welcome here and will be routed
to the right team, but the fix will land there, not in this client.

## Supported versions

Only the latest release gets security fixes. There is no backport
branch; upgrade to the newest version in
[Releases](https://github.com/newgrounds-inc/ngchat-cli/releases).
