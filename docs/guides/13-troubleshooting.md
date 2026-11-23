# Troubleshooting and FAQ

Answers to the problems people actually hit: slow first replies, a computer that went to sleep, WhatsApp dropping out, a busy port, where the files are, updating and removing.

## The first reply is slow

The AI model loads when the shop starts. The first question that needs the model can take up to a minute; later replies take a few seconds. Greetings, the catalog, opening hours, prices, stock, payment details and order status are answered from your data without the model, so they are quick even then. A slow computer that is also busy with heavy work (video calls, screen recording, games) is slower still.

## The computer must stay on and online

OwnShop AI runs on your computer. When the computer is off, asleep or offline, nobody gets a reply. Messages that arrived while it was off are not answered in a burst later.

- Set the computer not to sleep while plugged in: on Windows **Settings > System > Power**, on a Mac **System Settings > Battery** or **Energy**.
- Keep it plugged in and on the internet during your shop hours.
- OwnShop AI starts by itself when you log in, so after a restart, log in and it runs again.

## WhatsApp shows disconnected

- **"disconnected (...); reconnecting in 10 seconds"**: a short loss of internet on the computer or the phone. It reconnects by itself.
- **"not linked"**: the linked device was removed on the phone, or WhatsApp logged it out. Click **Link again** in **Settings** and scan the new code. See [Connect WhatsApp](/manual/whatsapp/).
- Keep the phone with the spare number charged and online now and then; WhatsApp logs out linked devices of a phone that stays offline for a long time.

## The page does not open, or the port is in use

The shop uses port 2007. If another program already uses it, OwnShop AI moves to the next free port (2008, 2009 ...) and remembers it. The desktop shortcut always points to the right one, so open the shop through the shortcut.

If no port from 2007 on is free, it shows "OwnShop AI found no free port from 2007 on. Close some programs and open OwnShop AI again."

If nothing opens at all, the shop is not running: start it from the Start menu (Windows) or Applications (Mac).

## Where my data lives

| | Program | Your shop's data |
|---|---|---|
| Windows | `%LOCALAPPDATA%\ownshop` | `%APPDATA%\ownshop` |
| Mac | `~/Applications/OwnShop AI.app` or `/Applications/OwnShop AI.app` | `~/Library/Application Support/ownshop` |

In the data folder:

- `ownshop.db`: the database with orders, customers, chats and settings,
- `products.xlsx`: your products file (the copy the shop reads),
- `images`: product photos,
- `orders.csv`: all orders, for Excel,
- `invoices` and `receipts`: the images sent to customers,
- `backups`: copies made before each Start over,
- `ownshop.log`: the log, useful when asking for help.

To paste a Windows folder name, press **Windows + R**, type `%APPDATA%\ownshop` and press Enter.

## How to update

Download the newest file from the [download section](/#download) and run it as in the install guide. On Windows the setup stops the running shop, replaces the program and starts it again. Your data folder is kept.

## Uninstall

**Windows:** open a Command Prompt in the folder with the setup file and run it with `/uninstall`, for example:

```bat
ownshop-setup-windows-amd64.exe /uninstall
```

It stops the shop and removes the program, the AI model, the Start menu entry, the startup entry and the desktop shortcut. Your shop's data stays in `%APPDATA%\ownshop`; delete that folder yourself if you want it gone.

**Mac:** quit **ownshop** in Activity Monitor, then delete **OwnShop AI** from Applications (and from `~/Applications`), the desktop link, and `~/Library/LaunchAgents/com.deemwar.ownshop.plist`. Delete `~/Library/Application Support/ownshop` too if you want your shop's data gone.

## Questions

**Is it really free?** Yes. No plan, no AI bill, no hosting. The only cost is keeping your computer on and online.

**Does the AI send my data to an AI company?** No. The model runs on your computer. Messages go only between your customers and your computer, through WhatsApp or Telegram.

**Which languages?** Ordering and replies work in English for now. Adding words from other languages to the `keywords` columns helps the shop find products and answers.

**Can it take payments?** No. It sends your payment details; you check the money and click **Payment received**.

**Can I use my main WhatsApp number?** We advise against it. Use a spare number, or Telegram.

**I need the official WhatsApp Business Platform.** OwnShop AI does not use it. For an official setup for a business, talk to [deemwar](https://deemwar.com).
