#!/bin/bash
# Scripted end-to-end run of `ownshop chat` (no phone needed): the customer side is the terminal fake channel. Owner clicks go through the real web handlers with curl.
# Usage: scripts/demo.sh  (from the repo root; needs models/qwen35-0.8b-q4km.gguf and llama-server). Output: docs/demo-run.txt
set -e
ROOT=$(pwd)
go build -o ownshop .
D=$(mktemp -d); cd "$D"
ln -s "$ROOT/models" models; cp -r "$ROOT/sample" sample; cp sample/products.xlsx products.xlsx
W="$ROOT/ownshop"
mkfifo fifo
$W import products.xlsx
$W chat --http :8091 --phone +31600000001 --name "Emma Novak" < fifo > chat.out 2>&1 &
CHAT=$!
exec 3>fifo
until curl -s -o /dev/null localhost:8091/settings; do sleep 1; done
step() { echo "$1" >&3; sleep ${2:-3}; }
owner() { echo "# owner: $1" >&3; sleep 1; }
click() {
  owner "Orders page: click $2"
  curl -s -o /dev/null -w "%{redirect_url}\n" -d to=$1 localhost:8091/orders/1042/status | sed 's/.*msg=/# web reply: /; s/+/ /g; s/%2C/,/g' >&3
  sleep 2
}
owner "Settings page: shop name, address, receipt footer, payment QR and payment link"
curl -s -o /dev/null -F shop_name="Green Leaf Organics" -F address="12 Market Lane, Utrecht" \
  -F currency=EUR -F receipt_footer="Thank you for shopping local." -F qr=@sample/payment-qr-demo.png \
  -F pay_link="https://pay.example.com/demo-shop" localhost:8091/settings
step "hello"
step "what are your opening hours?"
step "do you have honey?"
step "add 2 raw honey and 1 coconut oil"
step "1 coconut oil please"
step "Saturday 10-12"
step "YES" 4
click paid "Payment received"
click packed "Packed"
step "where is my order"
step "what is the weather today"
step "tell me a joke"
exec 3>&-
wait $CHAT
mkdir -p "$ROOT/docs"
{ echo "ownshop chat, scripted run $(date +%Y-%m-%d). Lines in [brackets] are debug: the raw model output and why a hand-off happened."; echo; cat chat.out; } > "$ROOT/docs/demo-run.txt"
cp receipts/receipt-1042.png "$ROOT/docs/demo-receipt-1042.png"
sqlite3 ownshop.db "select 'hand-offs: '||count(*) from handoffs; select 'invoice '||number||' paid='||paid from invoices; select 'stock raw honey (database): '||stock from products where name like 'Raw honey%';" >> "$ROOT/docs/demo-run.txt" 2>/dev/null || true
{ echo; echo "orders.csv (the file the owner opens):"; cat orders.csv; } >> "$ROOT/docs/demo-run.txt"
cd "$ROOT"; rm -rf "$D"
