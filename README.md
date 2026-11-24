# mailmigrate

Command-line tool to **migrate an entire mailbox between two different Microsoft
365 tenants** using the Microsoft Graph API — including Inbox, Archive, Deleted
Items, Junk, Sent, Drafts and all custom folders — while preserving the original
metadata (received/sent dates, read state, follow-up flags and categories).

Built in the same style as [enumdns](https://github.com/helviojunior/enumdns)
(Go + cobra + charmbracelet/log + gorm/sqlite). BSD-2-Clause.

> ⚠️ **Authorized use only.** Migrate mailboxes you own or are explicitly
> authorized to move. You interactively sign in as the source and destination
> users, so the tool only ever touches those two mailboxes.

## How it works

1. `auth-source` — interactive **device-code** login to the tenant you migrate
   **from**. The token (with refresh token) is cached locally.
2. `auth-destination` — same, for the tenant you migrate **to**.
3. `migrate` — after a confirmation, it walks every source folder, recreates the
   folder tree in the destination, and copies each message:
   - `GET /me/messages/{id}/$value` exports the **full original MIME** (headers,
     body, attachments, inline images) — this is what carries the original
     `Date:` / `Received:` headers.
   - `POST /me/mailFolders/{dest}/messages` (Content-Type `text/plain`, base64
     body) re-imports that MIME into the destination.
   - `PATCH /me/messages/{newId}` restores `isRead`, the follow-up flag and
     categories.

State is tracked in a SQLite DB (`~/.mailmigrate/state.db`), so a run is
**resumable and idempotent**: already-migrated messages are skipped unless you
pass `--force`.

## Install / build

```bash
make local          # builds ./build/mailmigrate_<os>_<arch>
# or
go build -o mailmigrate .
```

## Usage

```bash
# 1. Sign in to both tenants (opens a device-code prompt: URL + code)
mailmigrate auth-source
mailmigrate auth-destination

# 2. Preview what will be migrated
mailmigrate migrate --dry-run

# 3. Run it
mailmigrate migrate
# non-interactive:
mailmigrate migrate --yes --threads 4

# Only some folders (prefix match on the folder path):
mailmigrate migrate --include-folder Inbox --include-folder Archive
mailmigrate migrate --exclude-folder "Junk Email"
```

To migrate several users, repeat the three steps per user (one mailbox per run).

### Authentication options

By default it uses the first-party **Microsoft Graph Command Line Tools** public
client (`14d82eec-204b-4c2f-b7e8-296a70dad67c`), which needs no app registration.
If a tenant blocks it or requires admin consent for Mail scopes, register your
own multi-tenant app and pass it:

```bash
mailmigrate auth-source --client-id <your-app-id> --tenant contoso.onmicrosoft.com
```

Delegated scopes requested: `Mail.ReadWrite`, `MailboxSettings.Read`, `User.Read`.

## Limitations

- **Received-date fidelity** relies on Graph honouring the `Received:`/`Date:`
  headers of the re-imported MIME. Validate on a test mailbox first (compare
  `receivedDateTime`/`sentDateTime` on a few messages).
- **Recoverable Items (dumpster)** are not accessible via delegated Graph. The
  *Deleted Items* folder is migrated; permanently purged items are not.
- **Very large messages** may exceed the import endpoint limit; they are logged
  and recorded as `failed`, and the run continues.
- Scope is **e-mail only** (no calendar/contacts) and **one mailbox per run**.

## Files & state

| Path | Purpose |
|------|---------|
| `~/.mailmigrate/source.json` | Source token cache (OAuth tokens — keep private) |
| `~/.mailmigrate/destination.json` | Destination token cache |
| `~/.mailmigrate/state.db` | Migration progress (folders + per-message status) |

## Development

```bash
make hooks   # install the gofmt pre-commit hook
make fmt     # gofmt -s -w .
go test ./...
```
