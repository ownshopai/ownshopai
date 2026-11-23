package main

// The payment step and Settings show one way to pay at a time: four choice cards (UPI, QR code
// image, payment link, bank transfer) and only the chosen one's field. The shop still keeps every
// method that is set; posting one method changes only that method.

type payMethod struct{ Key, Label, Detail string }

var payMethods = []payMethod{{"upi", "UPI", "UPI ID, QR made per order"}, {"qr", "QR code image", "From your bank or wallet app"},
	{"link", "Payment link", "PayPal, Stripe, Wise..."}, {"bank", "Bank transfer", "Account details to copy"}}

// payKeys are the settings each method owns.
var payKeys = map[string][]string{"upi": {"upi_id", "upi_name"}, "qr": {"qr_path"}, "link": {"pay_link"}, "bank": {"pay_note"}}

// PayForm is what the payment controls show.
type PayForm struct {
	Method  string      // the card that is picked
	Methods []payMethod // the four cards
	Set     []payMethod // methods already set, Detail = their value
	Next    string      // a method not set yet, for "Add another"
}

func validPay(k string) bool { _, ok := payKeys[k]; return ok }

func (a *App) payForm(pick string, setup bool) PayForm {
	f := PayForm{Methods: payMethods}
	for _, m := range payMethods {
		v := a.S.Get(payKeys[m.Key][0])
		if m.Key == "qr" && v != "" {
			v = "image uploaded"
		}
		if v != "" {
			f.Set = append(f.Set, payMethod{m.Key, m.Label, v})
		} else if f.Next == "" {
			f.Next = m.Key
		}
	}
	switch {
	case validPay(pick):
		f.Method = pick
	case !setup && len(f.Set) > 0:
		f.Method = f.Set[0].Key
	case a.S.Get("currency") == "INR" || a.S.Get("currency") == "":
		f.Method = "upi"
	default:
		f.Method = "link"
	}
	return f
}
