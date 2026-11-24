// Props for a free-text control (note, title, filter, search box, name of a thing) that must not
// open the browser's autofill popup. Without an `autoComplete`/`name`, Chrome guesses a contact
// field and covers the dialog with the saved e-mail address and "Manage addresses…".
//
// Spread it on every `<input>` (text/search/url or no type) and `<textarea>` that is not a
// credential or real personal data: `<input {...NO_AUTOFILL} … />`. noAutofill.guard.test.ts
// fails on a control that has neither this nor an explicit `autoComplete`.
//
// "off" is what the Console already used on ~30 inputs. Chrome has ignored "off" for address
// autofill on fields it classifies as address/contact by label or placeholder; if that shows up on
// a field, change the value here (one place) instead of per input. Not verified in desktop
// Chrome from headless, which cannot show the popup.
export const NO_AUTOFILL = { autoComplete: "off" } as const;
