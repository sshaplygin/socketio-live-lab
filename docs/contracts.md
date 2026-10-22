# Live lab wire contract (schema 1)

Socket.IO namespace `/lab`, HTTP Engine.IO path `/socket.io/`, Engine.IO protocol
3 and Socket.IO protocol 4. The browser is pinned to JS 2.5.0. Use polling with
normal WebSocket upgrade. The maintained root Go module is pinned to commit
`0fa1ead74224`; see the compatibility note in the README.

The Transport control deliberately downgrades an established WebSocket connection
by reconnecting with `transports: ["polling"]` and `upgrade: false`. Engine.IO 3
does not define a reverse upgrade on the existing WebSocket session. The reverse
button reconnects with both transports and automatic upgrade enabled; the metric
only reports the actual engine transport. Failed WebSocket probes leave polling
working and show an explicit fallback status. This uses the pinned client’s
[documented transport options](https://socket.io/docs/v2/client-api/#new-Manager-url-options).
The choice persists in `live-lab-transport`. Identity and room are restored through
bootstrap; an unsent draft remains in the composer during a transport switch.

TypeScript DTOs live in the [portfolio frontend](https://github.com/sshaplygin/sshaplygin.dev/blob/main/src/lib/live-lab/types.ts). Go DTOs: [`internal/lab/types.go`](../internal/lab/types.go). Wire field names are camelCase.
Every client action takes one JSON object. ACKs use ordinary handler return values,
not the library's typed `AckEvent` error-first envelope:

```json
{"ok":true,"data":{"eventId":"server-generated","sequence":18}}
```

```json
{"ok":false,"error":{"code":"RATE_LIMITED","message":"Send up to five messages per ten seconds"}}
```

| Event | Direction | Request / result |
| --- | --- | --- |
| `lab:bootstrap` | Browser → server, ACK | `{visitorId?, nameStyle?, bootId?, positions?}` → Snapshot |
| `lab:presence` | Server → ready clients | Full Presence, monotonic `presenceRevision` within `bootId` |
| `lab:send` | Browser → server, ACK | `{clientMessageId,text,scope,roomId?}` → `{eventId,sequence}` |
| `lab:event` | Server → authorized recipients | LabEvent; no optimistic frontend insert |
| `lab:room:join` | Browser → server, ACK | `{roomId,bootId?,positions?}` → Snapshot after actual room update |
| `lab:probe` | Client → server, ACK | `{}` → `{serverTime,presence}`; never shared/human activity |
| `lab:resync` | Browser → server, ACK | `{bootId?,positions?}` → Snapshot |
| `lab:identity:shuffle` | Browser → server, ACK | `{nameStyle?}` → Identity, throttled |
| `lab:identity` | Server → same identity's tabs | Updated Identity |
| `lab:bot:echo` | Browser → server, ACK | `{text}` → `{correlationId}` |
| `lab:bot:request` | Server → authenticated ready Go process | `{correlationId,text}` |
| `lab:bot:reply` | Go process → server | `{correlationId,text}`; optional ordinary ACK |
| `lab:bot:chat` | Authenticated Go process → server, ACK | `{replyTo,text}` → `{eventId,sequence}`; reply to a recent human message |
| `lab:bot:result` | Server → originating browser only | `{correlationId,text,error?}` |
| `lab:bot:ready` | Authenticated Go process → server, ACK | `{}` → `{ready:true}`; joins developers |
| `lab:bot:status` | Server → ready clients | `{ready:boolean}`; presence is authoritative |

`Identity.nameStyle` is one of `systems` (default), `network`, `terminal`, or
`pioneers`. Each server-owned dictionary supplies 256 base names, with suffixes
for collisions. Unsupported styles return `INVALID_NAME_STYLE`. Bootstrap uses
the preference only for a new identity; existing identities remain authoritative.
Shuffle without a style preserves the current style, and a successful shuffle
updates every connected sibling tab. The browser saves the confirmed style locally.
The chat exposes only Shuffle name: each click picks a random style before asking
the server for a new name. There is no style selector in the interface.

The visible, connected playground probes every five seconds with at most one request
in flight and a five-second deadline. RTT measures the real acknowledgement with
`performance.now()`. The UI shows the last sample time; it clears stale samples
after fifteen seconds, on disconnect, and when hidden. Returning to the tab triggers
an immediate probe. Three consecutive timeouts show Degraded; a successful probe
restores Connected. Probes never create shared events or human activity.

The real Go client handles human `lab:event` messages in Global and its joined
`developers` room. It supports `/help`, `/ping`, `/joke`, `/quote` and greetings. Ordinary messages
and `/quote` receive an attributed quotation from a local six-entry pool of
scientists in Docker/Moby’s names dictionary. A synchronized shuffle deck exhausts
each cycle before repeating, and avoids immediate repeats at cycle boundaries.
Sources live beside the quotes; there is no LLM or external service. Both the client and server cap
automatic replies at one per five seconds. Explicit bot commands bypass this
cooldown and remain subject to the browser’s five-messages-per-ten-seconds limit.
The send ACK rejects commands when the bot is unavailable or outside its room.
Command buttons send directly and preserve the composer’s draft. The server requires a human source message
no older than ten seconds, membership in its scope, and at least one ready browser
still in that chat. It derives the reply scope from the source, enforces the
submission switch, and deduplicates against retained bot replies. Unrelated rooms,
system events and bot events cannot trigger chat replies. No periodic chatter is
generated. Manual echoes remain separate from this chat rate limit.

Chat reply events include optional `replyTo`, the original event ID. They keep
`type: bot` and `sender.type: go-client`; they advance shared history but never
human activity. The UI marks own messages as You using the server's public identity
ID, other browsers as Participant, and Go messages as Go bot, with distinct styling
and a quoted reply context when the original is still in local history.

`Snapshot` includes `bootId`, assigned `identity`, full `presence`, the three
available `rooms`, confirmed `roomId`, `positions` (per-stream high-water marks),
retained authorized `history`, and `truncated`. No history for unrelated rooms is
returned. Changing rooms calls the library's Leave/Join operations and updates
registry membership under the same application lock. Queued events are filtered
by current membership in the frontend; previously received public room history
is replaced when the room snapshot is installed.

Global and room sequences are independent. Acceptance, sequence assignment,
history append, and delivery enqueue occur under the same lock. Delivery workers
perform network queue writes outside it. A slow consumer is disconnected and
must resynchronize; an ACK promises acceptance into bounded in-memory history,
not durable or guaranteed recipient delivery.

The browser buffers live events while obtaining a snapshot, installs its stream
positions, then applies only subsequent sequences. IDs and sequence high-water
marks deduplicate. Gaps request a fresh snapshot. A different boot ID clears old
positions and presence. Retention gaps are explicitly shown. Presence revisions
and generated timestamps prevent older snapshots overwriting newer information.
Local Clear my view retains high-water marks and never clears server history.

The chat exposes Everyone (`global`), Developers (`developers`), and Experiments
(`experiments`), with an audience explanation beneath each chat title. The legacy
`general` room remains available in the protocol but is absent from navigation;
saved General selections return to Everyone without rejoining that room.

The chat displays only the selected channel: Everyone filters global events; a room
filters that room's events. Selecting a different room waits for its authoritative
join snapshot before changing the active channel or send destination. Selecting
Everyone keeps the current room subscription available in the background. Reconnect
restores membership before displaying a saved room. Hidden-channel arrivals update
the bounded local state without raising the visible New messages indicator.
The composer sends to the selected channel, supports Enter/Shift+Enter, respects
IME composition, and retains the same message ID when retrying an unconfirmed send.

Names are cosmetic. `visitorId` is a validated random opaque hint in localStorage,
not an authorization credential. `publicId` is separate display metadata, used for
sender ownership and grouped tab counts. The HTTP status response exposes neither
identifier. A bot authenticates using a server-only Authorization bearer token on
its connection; browsers cannot claim the Go-client type through event payloads.

`lab:bot:echo` acceptance updates human activity. Readiness, probe, join, bootstrap,
rename, reconnect, history fetch, HTTP polling and bot replies do not. Echo forwarding
is asynchronous: handlers do not block waiting for work on their own dispatcher.
A five-second server deadline bounds pending echoes and disconnect rejects them.
The browser registers its result listener before requesting, buffers early results,
and measures end-to-end duration with `performance.now()`.

HTTP status: `GET /api/live-lab/status` returns schema version, boot ID, readiness,
server `generatedAt`, total/browser/Go counts, presence revision, nullable last
human/event timestamps, room counts, bot readiness, and submissions enabled.
`Cache-Control: no-store` is mandatory. HTTP status is separate from personal
application readiness. A received fresh HTTP snapshot never marks a socket Connected.

The preview polls every 30 seconds only while visible in a visible tab, backs off
to at most 120 seconds on failure, and aborts on hide/disposal. Counts are displayed
only within 45 seconds of receipt; status becomes unknown after 60 seconds.
Request generations reject late HTTP responses. Activity uses server timestamps
and a measured server-clock offset. The intervals are configurable via the poller
policy in `src/lib/live-lab/state.ts`.
