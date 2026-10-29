# MCP: AI access to the budget

The server has a [Model Context Protocol](https://modelcontextprotocol.io) endpoint at `/mcp`,
so an AI client — Claude Code, the Claude desktop app, anything that speaks MCP — can read
the budget and do the categorising chore, acting as one of the household.

## Connecting

1. Sign in, open **Settings → AI access**, and create a token. Name it after the device or app
   it is for, and choose how long it lives (30 days, 90 days or a year).
2. The token is shown **once**, with the command to use it:

   ```sh
   claude mcp add --transport http budget https://budget.example.com/mcp \
     --header "Authorization: Bearer bgt_…"
   ```

   Any other client: endpoint `https://<host>/mcp`, header `Authorization: Bearer bgt_…`.
3. Ask things like "summarise October against our targets", "categorise last month's
   uncategorised transactions", or "make rules for the merchants we use every month".

Revoke a token on the same page. **Changing your password revokes every token you hold**, as
it ends your sessions.

## Tools

| Tool | Kind | What it does |
|---|---|---|
| `list_accounts` | read | Accounts, kinds, transaction counts, date ranges (last 4 of the number only). |
| `list_categories` | read | Categories and their kind (expense / income / transfer). |
| `list_rules` | read | Rules, with how many transactions each matches. |
| `search_transactions` | read | Month or date range, account, category, uncategorised, text. Total and in/out over every match; at most 200 rows a page. |
| `spending_summary` | read | One month against its targets — the same `budget.Summary` the overview page shows. top-level `remaining` is target minus spent over the expense categories that have a target (uncategorised money not counted; over-spent categories subtract). |
| `list_imports` | read | Recent imports and their counts or errors. |
| `categorise_transactions` | write | Up to 100 `{transaction_id, category_id}` changes, all or nothing. |
| `create_rule` / `update_rule` / `delete_rule` | write | Rules, optionally applied to uncategorised transactions now. |
| `create_category` / `update_category` / `archive_category` | write | Categories; archived, never deleted. |
| `set_target` | write | A category's target for a month, or clear it. |
| `update_account` | write | An account's name or kind. |

Amounts are decimal strings (`"-89.90"`, negative is money out) with integer cents beside
them; dates are `YYYY-MM-DD`, months `YYYY-MM`.

## Deliberately not available

Members, tokens, passwords, uploads and imports, deleting transactions, triggering the FNB
fetch, settings. A tool that could create an account or a token would be a way to escalate; a
tool that could trigger a bank login is one an injected instruction could abuse.

## The security model

- **A token is its person acting.** Both members can do everything, so there are no scopes;
  the tool set above is the boundary.
- **Bearer only.** `/mcp` reads the `Authorization` header and nothing else — a session cookie
  is never a credential here (a test pins this). That is also why `/mcp` sits outside the CSRF
  middleware: there is no ambient credential for a forged request to carry.
- **Cross-origin browser requests are refused** (stdlib `http.CrossOriginProtection`), so a web
  page cannot drive the endpoint. MCP clients send no `Origin` and are unaffected.
- **Rate limits**: a per-IP ceiling on all calls, checked before any token lookup, and a much
  tighter per-IP limit charged only by *failed* tokens, so the endpoint is not a guessing oracle.
- **Tokens**: `bgt_` + 32 random bytes, only the sha256 stored, expiring (at most 366 days),
  revoked by a password change, refused while the account is on a temporary password.
- **Prompt injection.** Transaction descriptions are written by merchants and reach the model.
  The server instructions and tool descriptions say they are data, never instructions, and they
  are returned as plain JSON fields. More importantly, what an injected instruction could do is
  bounded by the tool set — budgeting data only — and **every write is recorded** in the audit
  log with the token's name and the value it replaced. **Settings → Activity** shows the AI's
  changes; a categorisation can be **undone** there (refused if the transaction has been changed
  again since, so an undo never clobbers a newer choice).
- Stateless Streamable HTTP with JSON responses: every call is verified afresh, and a revoked
  token stops working on its next call.
