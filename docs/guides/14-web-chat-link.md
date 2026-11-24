# Put your chat on the web

Give customers a link they open in any browser to chat with your shop, from a phone or a computer. No website or domain needed: OwnShop AI uses Cloudflare's free tunnel, and your shop page stays on this computer only.

## 1. Install Cloudflare's tunnel program (once)

OwnShop AI uses **cloudflared**, Cloudflare's free tunnel program. It is not inside OwnShop AI; install it once:

1. **Windows:** open **Command Prompt** and run `winget install --id Cloudflare.cloudflared`
2. **Mac:** open **Terminal** and run `brew install cloudflared`
3. Or download it from Cloudflare's page: https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/
4. In OwnShop AI, open **Settings**, scroll to **Chat channels** and find the card **Website (your own public chat link)**. Click **Check again**. The card shows the version it found.

## 2. Get your free link

Your link is `yourname.ownshopai.store`. It stays the same after a restart, so printed QR codes keep working.

1. On the Website card, click **Sign in with Google** and finish on the page that opens in your browser (the first time it asks for a few shop details).
2. Come back to OwnShop AI. Type the name you want; the card says whether it is free.
3. Click **Get my link**, then **Make my web chat public**.

If the card says **Coming soon**, the free link service cannot be reached right now; try again later.

## 3. Share the link

1. The card shows your link, a **Copy** button and a QR code. The Orders page shows the link at the top too.
2. Put the link on your shop sign, cards, social media profile or messages. Customers can also scan the QR code.
3. Click **Stop** to take the link offline. If the link was on when OwnShop AI closes, it starts again by itself next time.

## 4. What your customers see

1. They open the link and type their **phone number** or **email** (one is enough), and their name if they like. No password.
2. They chat with the shop like on WhatsApp. Orders and bookings show up on your Orders page like any other.
3. The browser remembers them, so next time they go straight to the chat. **Not you? Start again** lets someone else use the same browser.
4. **Install as an app:** on Android and computers they can tap **Add to home screen**. On iPhone: Share, then **Add to Home Screen**.
5. **Notifications:** they can tap **Turn on notifications** to get a message when the shop replies while the page is closed. On iPhone this works only after adding it to the home screen (iOS 16.4 or later).

On the **Users** page you see each web customer with name, phone or email, first and last visit, and **Block** / **Unblock**. A block works on every channel.

## Honest limits

- **The computer must stay on** with OwnShop AI running, or the link does not answer.
- **Cloudflare carries the traffic** between customers and your computer, so Cloudflare can see the chats.
- **The phone number or email is not checked.** Anyone can type any number; the owner sees "web, phone not verified". Each browser is its own customer: the same number in another browser is a different chat, and it is never mixed with WhatsApp or Telegram.
- There are limits against spam: about 10 sign-ins per 10 minutes per address, about 12 messages per minute per browser, messages up to 1000 characters, and your daily send cap in Settings.

## If it goes wrong

- **The card says "Needs Cloudflare's free tunnel program".** Install it (step 1) and click **Check again**.
- **The link says it cannot be reached.** Wait half a minute after starting; check the computer is online. Click **Stop**, then start it again.
- **Someone is sending spam.** Block them on the Users page.
- **The card says your free link is off.** The link service turns off links that were not used for 90 days, or that broke the rules (https://ownshopai.store/terms/#aup). Write to io@deemwar.com.
- **Deleting the shop** (Start over) gives the free link name back first.
