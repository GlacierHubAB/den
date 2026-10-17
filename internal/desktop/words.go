package desktop

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// sensitiveWords are control names that make a click ask, whatever the
// app: sending, submitting, deleting, paying, signing in, and their kin,
// in the languages a desktop here is likely to show. Matched as whole
// words on folded text (Sensitive). Over-matching asks; it never allows,
// and the list is an extra reason to ask, not the main protection.
var sensitiveWords = []string{
	// English
	"send", "submit", "delete", "remove", "pay", "buy", "purchase", "order", "checkout", "confirm",
	"transfer", "publish", "post", "share", "sign in", "log in", "login", "signin", "sign up", "signup",
	"reply", "forward", "authorize", "authorise", "approve", "accept", "install", "erase", "discard",
	// Swedish
	"skicka", "radera", "ta bort", "betala", "köp", "bekräfta", "logga in", "publicera", "dela", "svara", "godkänn",
	// German
	"senden", "absenden", "löschen", "entfernen", "bezahlen", "zahlen", "kaufen", "bestätigen", "anmelden",
	"einloggen", "veröffentlichen", "teilen", "antworten",
	// French
	"envoyer", "soumettre", "supprimer", "payer", "acheter", "confirmer", "se connecter", "connexion",
	"publier", "partager", "répondre",
	// Spanish and Portuguese
	"enviar", "eliminar", "borrar", "pagar", "comprar", "confirmar", "iniciar sesión", "acceder",
	"publicar", "compartir", "responder", "excluir", "entrar",
	// Italian
	"invia", "inviare", "elimina", "cancella", "paga", "acquista", "conferma", "accedi", "pubblica", "condividi", "rispondi",
	// Dutch
	"verzenden", "versturen", "verwijderen", "betalen", "kopen", "bevestigen", "inloggen", "aanmelden", "publiceren", "delen",
	// Russian
	"отправить", "удалить", "оплатить", "купить", "подтвердить", "войти", "опубликовать", "ответить",
	// Japanese and Chinese (no word boundaries: matched as substrings)
	"送信", "削除", "支払", "購入", "確認", "ログイン", "投稿", "発送", "发送", "删除", "支付", "购买", "确认", "登录", "提交", "发布",
}

// Sensitive reports whether a control's name holds one of the sensitive
// words, and which. The name is NFKC-normalised and case-folded, with
// invisible and bidi formatting characters removed, so look-alike
// spellings and hidden characters do not slip past.
func Sensitive(name string) (string, bool) {
	text := fold(name)
	if text == "" {
		return "", false
	}
	for _, w := range sensitiveWords {
		fw := fold(w)
		if hasWord(text, fw) {
			return w, true
		}
	}
	return "", false
}

// fold is the text Sensitive matches on: NFKC, lower case, formatting
// characters dropped, every run of spaces, punctuation or symbols one
// space.
func fold(s string) string {
	s = norm.NFKC.String(s)
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.Is(unicode.Cf, r):
			// Zero-width, bidi and other format characters never count.
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r):
			b.WriteRune(r)
			space = false
		default:
			if !space {
				b.WriteByte(' ')
				space = true
			}
		}
	}
	return norm.NFC.String(strings.TrimSpace(b.String()))
}

// hasWord reports whether w occurs in text on word boundaries; a word in
// a script written without spaces matches anywhere.
func hasWord(text, w string) bool {
	if !spaced(w) {
		return strings.Contains(text, w)
	}
	for i := 0; ; {
		j := strings.Index(text[i:], w)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(w)
		if (start == 0 || text[start-1] == ' ') && (end == len(text) || text[end] == ' ') {
			return true
		}
		i = start + 1
	}
}

// spaced reports whether a word's script separates words with spaces.
func spaced(w string) bool {
	for _, r := range w {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) {
			return false
		}
	}
	return true
}
