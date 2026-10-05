# Remote MCP transport

Connector implementations use this package with a host-authorized HTTP client for one user and environment. It uses the official [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk) at the version pinned in the module lockfile. HTTP endpoints use [Streamable HTTP](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports).

The package negotiates the protocol, reads paginated tool catalogues, and executes a tool once. The pinned SDK first attempts the `2026-07-28` discovery handshake and falls back to legacy initialization when needed. Tests exercise a server limited to `2025-11-25` with both JSON and SSE responses, checking the actual protocol headers and content types. SDK upgrades can change the initially offered protocol version.

The client preserves structured results and separates protocol failure, tool failure, and additional user interaction. Catalogues have page/item limits and repeated-cursor detection. Each HTTP response body and SSE event is capped at 8 MiB; this is not an aggregate catalogue-size limit. It does not infer authority from a tool's presence.

Credentials, approved destinations, project permission, deferred vendor tool discovery, field mapping, and operation receipts belong to the host or connector. Supply the host's credential-aware transport; this package has no credential store. It ignores cookie jars and refuses redirects. The supplied transport must honor request contexts and must not introduce mutation replays or browser-session cookies. Local HTTP requires an explicit option and a loopback address.

Calls have deadlines. There is no automatic mutation retry, REST fallback, child process, roots exposure, sampling, or automatic elicitation. The SDK may resume an interrupted SSE response with a GET carrying its last event ID; it does not replay the tool call. A transport failure during mutation reports an unknown outcome: the connector must recover the operation's durable receipt before deciding whether replay is safe. Tool failures and input requests retain the response for the connector to interpret; their presence alone does not prove that no partial write occurred. Raw errors and tool results can contain provider data and must not be logged as diagnostic text.

The transport does not supply a durable change feed. A provider must separately implement and advertise its actual change-cursor and synchronization guarantees.
