# Put your chat on the web

Give customers a link they open in any browser to chat with your shop, from a phone or a computer. No website or domain needed: OwnShop AI uses Cloudflare's free tunnel, and your shop page stays on this computer only.

## 1. Install Cloudflare's tunnel program (once)

OwnShop AI uses **cloudflared**, Cloudflare's free tunnel program. It is not inside OwnShop AI; install it once:

1. **Windows:** open **Command Prompt** and run `winget install --id Cloudflare.cloudflared`
2. **Mac:** open **Terminal** and run `brew install cloudflared`
3. Or download it from Cloudflare's page: https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/
4. In OwnShop AI, open **Settings**, scroll to **Chat channels** and find the card **Website (your own public chat link)**. Click **Check again**. The card shows the version it found.

## 2. Pick a kind of link

The card offers three kinds of link. Pick one:

1. **Free fixed link** (coming soon; its link service is not live yet): `yourname.ownshopai.store`. It stays the same after a restart, so printed QR codes keep working. Click **Sign in with Google**, finish on the page that opens in your browser, come back, type a name and click **Get my link**.
2. **Quick link** (no sign-up): click **Make my web chat public**. Cloudflare gives a random link like `https://some-words.trycloudflare.com`. It **changes every time OwnShop AI or the computer restarts**, so share the new one after a restart.
3. **Advanced: your own domain.** See step 5.

## 3. Share the link

1. The card shows your link, a **Copy** button and a QR code. The Orders page shows the link at the top too.
2. Put the link on your shop sign, cards, social media profile or messages. Customers can also scan the QR code.
3. Click **Stop** to take the link offline. If the link was on when OwnShop AI closes, it starts again by itself next time.

## 4. What your customers see

1. They open the link and type their **name** and **phone number**. No password.
2. They chat with the shop like on WhatsApp. Orders and bookings show up on your Orders page like any other.
3. The browser remembers them, so next time they go straight to the chat. **Not you? Start again** lets someone else use the same browser.
4. **Install as an app:** on Android and computers they can tap **Add to home screen**. On iPhone: Share, then **Add to Home Screen**.
5. **Notifications:** they can tap **Turn on notifications** to get a message when the shop replies while the page is closed. On iPhone this works only after adding it to the home screen (iOS 16.4 or later).

On the **Users** page you see each web customer with name, phone, first and last visit, and **Block** / **Unblock**. A block works on every channel.

## 5. Advanced: keep the same link on your own domain

You need a free Cloudflare account and a domain that uses Cloudflare.

1. Sign in at dash.cloudflare.com and open **Zero Trust**.
2. Go to **Networks** > **Tunnels** and click **Create a tunnel**. Choose **Cloudflared**, give it a name (for example `myshop-chat`) and save.
3. On the install page Cloudflare shows a command with a long token after `--token`. Copy only the token. You do not need to run the command.
4. Click **Next** to **Public Hostname**. Pick a subdomain (for example `chat`) and your domain. Under **Service**, choose **HTTP** and type `127.0.0.1:2008` (the card shows the exact address). Save.
5. In OwnShop AI, open **Advanced** on the Website card, paste the token, type the hostname (for example `chat.myshop.com`) and click **Save and use my own domain**.
6. The card shows **Token saved**. The token is never shown again. **Remove** deletes it.

## Honest limits

- **The computer must stay on** with OwnShop AI running, or the link does not answer.
- **Cloudflare carries the traffic** between customers and your computer, so Cloudflare can see the chats.
- **The phone number is not checked.** Anyone can type any number; the owner sees "web, phone not verified". Each browser is its own customer: the same number in another browser is a different chat, and it is never mixed with WhatsApp or Telegram.
- The quick link changes after a restart. Use the free fixed link or your own domain if that matters.
- There are limits against spam: about 10 sign-ins per 10 minutes per address, about 12 messages per minute per browser, messages up to 1000 characters, and your daily send cap in Settings.

## If it goes wrong

- **The card says "Needs Cloudflare's free tunnel program".** Install it (step 1) and click **Check again**.
- **The link says it cannot be reached.** Wait half a minute after starting; check the computer is online. Click **Stop**, then start it again.
- **Customers have an old link.** A quick link changes after a restart. Share the new one, or use a fixed link.
- **Someone is sending spam.** Block them on the Users page.
- **The card says your free link is off.** The link service turns off links that were not used for 90 days, or that broke the rules (https://ownshopai.store/terms/#aup). Write to io@deemwar.com.
- **Deleting the shop** (Start over) gives the free link name back first.
