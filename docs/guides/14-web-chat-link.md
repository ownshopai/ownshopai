# Put your chat on the web

Give customers a link they open in any browser to chat with your shop, from a phone or a computer. No website, domain or extra program needed: OwnShop AI keeps one connection open to the OwnShop AI link service, which passes your customers' chats to it. Your shop page stays on this computer only; only the customer chat is reachable.

## 1. Get your free link

Your link is `yourname.ownshopai.store`. It stays the same after a restart, so printed QR codes keep working.

1. In OwnShop AI, open **Settings**, scroll to **Chat channels** and find the card **Website (your own public chat link)**. Click **Sign in with Google** and finish on the page that opens in your browser (the first time it asks for a few shop details).
2. Come back to OwnShop AI. Type the name you want; the card says whether it is free.
3. Click **Get my link**. The link goes online by itself: the card says **connecting**, then **online**.

Nothing to install and no router settings: the connection goes out from OwnShop AI, so it works behind any home router, on office Wi-Fi and on mobile data (also on an Android phone running OwnShop AI in Termux). If you sign out of the link service, the link goes offline until you sign in again; the name stays yours.

While the card says **Coming soon**, the free link service is not open yet or cannot be reached; the card works as soon as it answers.

## 2. Share the link

1. The card shows your link, a **Copy** button and a QR code. The Orders page shows the link at the top too.
2. Put the link on your shop sign, cards, social media profile or messages. Customers can also scan the QR code.
3. Click **Take offline** to stop it, **Put my link online** to bring it back. If the link was on when OwnShop AI closes, it comes back by itself next time. **Release this link** gives the name back.

## 3. What your customers see

1. They open the link and type their **phone number** or **email** (one is enough), and their name if they like. No password.
2. They chat with the shop like on WhatsApp. Orders and bookings show up on your Orders page like any other.
3. The browser remembers them, so next time they go straight to the chat. **Not you? Start again** lets someone else use the same browser.
4. **Install as an app:** on Android and computers they can tap **Add to home screen**. On iPhone: Share, then **Add to Home Screen**.
5. **Notifications:** they can tap **Turn on notifications** to get a message when the shop replies while the page is closed. On iPhone this works only after adding it to the home screen (iOS 16.4 or later).

On the **Users** page you see each web customer with name, phone or email, provider **Web (not verified)**, first and last visit, and **Ban** / **Unban**. A ban works on every channel.

## Honest limits

- **The computer (or phone) must stay on** with OwnShop AI running and online. While it is not, customers see "This shop is offline right now".
- **The OwnShop AI link service and Cloudflare carry the traffic** between customers and your computer, so they can see the chats as they pass.
- **The phone number or email is not checked.** Anyone can type any number; the owner sees "web, phone not verified". Each browser is its own customer: the same number in another browser is a different chat, and it is never mixed with WhatsApp or Telegram.
- There are limits against spam: about 10 sign-ins per 10 minutes per address, about 12 messages per minute per browser, messages up to 1000 characters, and your daily send cap in Settings.

## If it goes wrong

- **The card says connecting for a long time.** Check the computer is online. OwnShop AI keeps retrying by itself (up to a minute between tries); **Take offline** and **Put my link online** tries again now.
- **The card says the link service does not know this link.** Release it and get a new name.
- **Someone is sending spam.** Ban them on the Users page.
- **The card says your free link is off.** The link service turns off links that were not used for 90 days, or that broke the rules (https://ownshopai.store/terms/#aup). Write to io@deemwar.com.
- **Deleting the shop** (Start over) gives the free link name back first.

## Serve it yourself (advanced, not supported)

If you would rather not use the free link, you can put the chat online with a tunnel program of your own. Point it at the **public chat port 2008 only**, never at 2007 (2007 is your shop page with your orders and settings):

- ngrok: `ngrok http 2008`
- cloudflared: `cloudflared tunnel --url http://127.0.0.1:2008`

Port 2008 serves only the customer chat; every owner page answers "not found" there. The computer must stay on, and we do not support this setup: if it breaks, use the free link above.
