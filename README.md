# OwnShop AI

A free, open-source shop assistant for small businesses that sell through chat. It runs on your own computer with a
small local AI model. You don't need a server, a cloud account, an AI API key or a monthly plan.

Customers write to your shop on Telegram, WhatsApp or your website chat. OwnShop AI answers questions from your FAQ,
finds products, builds the order, asks for a delivery slot, confirms when the customer says YES, then sends the invoice
and how to pay: your payment QR image, your payment link (Stripe, PayPal, Wise, Revolut, a bank link, any URL), or both.
You click **Payment received**, **Packed**, **Shipped**, **Delivered** or **Cancel**, and each click messages the customer.

- Website: https://ownshopai.store
- Manual (step by step, with screenshots): https://ownshopai.store/manual/

> **Read this first.**
> - **Telegram** uses the official Telegram Bot API. It is free.
> - **WhatsApp** is connected through **wacli**, an **unofficial** WhatsApp Web client. It is not the WhatsApp Business
>   Platform, and using it **may violate WhatsApp's terms**. **Your number can be banned.** Use a **spare number** and
>   use it responsibly. You are responsible for how you use it.
> - No warranty (see [LICENSE](LICENSE)). Not affiliated with, endorsed by, or sponsored by WhatsApp, Meta or Telegram.

## Quick start

Download the installer for your computer from https://ownshopai.store/#download:

- **Mac** (Apple Silicon): https://ownshopai.store/download/mac/
- **Windows** x64: https://ownshopai.store/download/windows-x64/
- **Windows** ARM64: https://ownshopai.store/download/windows-arm64/

Each download carries OwnShop AI, its WhatsApp client (wacli), the model server (llama.cpp `llama-server`) and the model
file. Nothing else to install. On the Mac, until the app is notarized, macOS asks once: right-click, Open.

## Requirements

- macOS on Apple Silicon, or Windows 10/11 (x64 or ARM64).
- About 600 MB of disk and about 1 GB of free memory while chatting. A reply takes about 0.4 s on a recent Mac.
- No API key, no account, no internet AI service. The model runs locally.
- For WhatsApp: a spare phone number. For Telegram: a bot token from @BotFather.
- To build from source: Go (see `go.mod`), [Task](https://taskfile.dev), and for the installers cmake and zig.

## Using it

1. **Double-click OwnShop AI.** It opens your browser at http://localhost:2007 (if another program uses that port, it takes the next free one, 2008, 2009, ..., and remembers it). It also starts by itself when you log in, and
   puts an *OwnShop AI* shortcut on your desktop that opens the shop page. Double-clicking it again just opens the page.
2. The page walks you through four steps:
   1. **Shop**: your shop name and currency.
   2. **Products**: upload one Excel file. Not sure what it looks like? Download the example file on that page.
   3. **Payment**: your payment QR image and/or a payment link.
   4. **Chat channels**: *Connect Telegram* (paste the token @BotFather gives you; the page shows the steps) or
      *Connect WhatsApp* (a pairing code appears on the page; scan it from the spare phone under Linked devices).
      You can connect several WhatsApp numbers and add more later in Settings.
3. After that you use four pages: **Orders**, **Products**, **Chats** (messages the shop was not sure about, plus the recent
   messages) and **Settings**.

There is nothing to type in a terminal, and no username or password: the page only listens on your own computer.

### Your two files

OwnShop AI keeps its own database, but the files you work with are plain files in the OwnShop AI folder
(`~/Library/Application Support/ownshop` on a Mac, `%AppData%\ownshop` on Windows):

| file | what it is |
|---|---|
| `products.xlsx` | Sheet **Products** (sku, name, description, price, stock, keywords), sheet **FAQ** (question, answer, keywords), optional sheet **Delivery slots** (label, capacity). Keep editing it in Excel. When you save it, OwnShop AI reads it again (or click *I updated my file* on the Products page). After each order OwnShop AI writes the new stock back into it. A product you delete from the file is no longer sold. |
| `orders.csv` | One row per order: order, date, customer, channel, items, total, status, invoice. Kept up to date; open it any time. |

If the file is open in Excel and cannot be written, OwnShop AI keeps the newer stock, tells you on the Products page, and writes
it as soon as it can, so it never sells stock you no longer have.

## How it works

- **One program.** OwnShop AI serves the owner's web page, runs the chat channels, and keeps its data in one SQLite file next
  to your two files.
- **Chat channels.** Every channel is a small adapter with the same shape (start, receive, send text, send image, status).
  Each customer is remembered with the channel and number they wrote to, and every reply and order update goes back on that
  same channel and number.
  - **Telegram**: official Bot API with long polling (`getUpdates`), `sendMessage`, `sendPhoto` and the typing indicator. Only private chats are answered. The bot token is stored locally, shown masked, and never
    logged.
  - **WhatsApp**: each number has its own wacli store inside OwnShop AI's data folder (`<data>/wacli/<name>`), so OwnShop AI never
    reads or edits `~/.wacli` or the wacli accounts other programs on the machine use. OwnShop AI runs
    `wacli --store <data>/wacli/<name> auth --qr-format text --events`
    to pair (the pairing code is drawn on the page), then keeps one follower per number:
    `wacli --store <data>/wacli/<name> sync --follow --webhook http://127.0.0.1:<port>/wacli/<name> --webhook-allow-private --webhook-secret <random> --webhook-events message --events`.
    wacli posts each new message to a listener OwnShop AI opens on 127.0.0.1 only; OwnShop AI checks the signature, answers 200 at
    once and handles the message after. Replies go out with `wacli send text` / `wacli send file --as image`, and
    `wacli presence typing`; wacli hands them to the running follower, which owns the number's store lock. If another program
    already holds that lock, OwnShop AI waits and retries and never stops it. Groups, broadcasts and your own messages are ignored.
- **The model only picks a tool.** OwnShop AI starts `llama-server` with Qwen3.5 0.8B and gives it 10 shop tools. Every reply is
  a fixed template filled from your data, never free model text, so the bot cannot invent a price or a policy. If the model
  cannot start, the shop still runs and every message comes to you under Chats.
- **Go checks every choice.** An order is confirmed only on a real YES with a cart waiting, a customer only ever sees their own
  order, and a product, slot or FAQ answer that is not in your data becomes a hand-off: the customer gets "the shop will
  reply shortly" and you answer from the Chats page.
- **Sending limits, per channel number or bot.** At most one message every 2 seconds per chat, a short "typing..." pause
  where the channel supports it, and a daily send cap (Settings, default 200 per number or bot). Messages that arrived
  while OwnShop AI was off are not answered in a burst.
- **Order statuses**: Waiting for payment, Payment received, Packed, Shipped, Delivered, Cancelled. Cancel puts the items
  back in stock; any refund is between you and the customer.

## Honest limits

- **Not a mass-messaging or broadcast tool.** It only replies to people who wrote to the shop first. The only messages it
  sends on its own are updates on that customer's own order.
- **Not a payment processor.** It never touches money. You check your bank and click Payment received.
- **Not a CRM.** One shop, one computer.
- **The model is small.** It picks the right tool about 2 times in 3 (below); every wrong pick that the checks catch goes
  to you on the Chats page instead of being acted on.
- **WhatsApp is unofficial** (see the warning above).
- Replies are in English for now. The Mac app is not notarized yet.

## How good is the model?

Qwen3.5 0.8B is small and untuned. `task eval` scores it on 44 realistic customer messages
([docs/eval.txt](docs/eval.txt)): **30/44 tool choices right (68%)**, 388 ms average per message. It is strong on FAQ, product
search and order status. It is weak on short replies ("monday evening works"), on "I'll take 5 oats" (it searches instead
of adding), and on off-topic chat. The Go checks above catch most wrong picks and hand them to you instead of acting.

**Other languages.** Tested on 24 messages in Spanish, German, French, Dutch, Portuguese and Arabic: the model picked the
right tool for 13/24. With the shop's own words in that language added to the sheet keywords (for example `miel honig mel`
for honey), 14 of 18 questions and searches got a real answer. Ordering works best in English, and replies are in English
for now.

## Build from source

```sh
task check   # go vet + go test (fake chat channel, a stub wacli program, a fake Telegram server)
task build   # dist/ownshop-darwin-arm64, dist/ownshop-windows-amd64.exe, dist/ownshop-windows-arm64.exe
task package # the installers, models included: dist/ownshop-macos-arm64.dmg, dist/ownshop-setup-windows-{amd64,arm64}.exe
task eval    # needs task bundle-deps and the model; writes docs/eval.txt
ownshop serve --db ./shop/ownshop.db   # the shop in the foreground, files next to the database
ownshop chat                           # be the customer in the terminal (a fake channel)
scripts/demo.sh                        # a scripted order end to end -> docs/demo-run.txt
```

A plain `go build` gives a program without the helpers inside; point the `wacli_bin` and `llama_server` settings at
your own copies. `task bundle-deps` (`scripts/bundle-deps.sh`) builds the two helpers into `bundled/`: wacli from
https://github.com/openclaw/wacli at the commit in `Taskfile.yml` (`WACLI_COMMIT`), and llama-server from llama.cpp
`b9620`. `-tags bundle` embeds them. The model file is looked up at `models/<file>`, next to the program, in the Mac app's
`Contents/Resources/models`, and in `<app data>/models`; it is not in this repository.

## License

OwnShop AI's own code is under the [MIT License](LICENSE).

The installers also carry two separate programs that OwnShop AI starts as child processes; they are not linked into it:

- **wacli** (MIT License). As built, wacli includes whatsmeow (MPL-2.0) and go.mau.fi/libsignal (**GPL-3.0**), so the
  wacli program inside the installers is distributed under the GPL-3.0. Its complete source is
  https://github.com/openclaw/wacli at commit `5578d1b6892f50a8f2329b11a6b6e27a1a81f88b`.
- **llama.cpp** `llama-server` (MIT License), https://github.com/ggml-org/llama.cpp tag `b9620`.

The model weights (Qwen3.5 0.8B) are under the Apache License 2.0. Every component and its license is listed in
[THIRD-PARTY-NOTICES](THIRD-PARTY-NOTICES) and [CREDITS](CREDITS).
