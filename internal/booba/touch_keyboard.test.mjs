import test from 'node:test';
import assert from 'node:assert/strict';
import { installTerminalKeyboard } from './touch_keyboard.mjs';

class Element extends EventTarget {
    attrs = new Map(); readOnly = false;
    setAttribute(key, value) { this.attrs.set(key, value); }
    getAttribute(key) { return this.attrs.get(key) ?? null; }
    removeAttribute(key) { this.attrs.delete(key); }
}
function fixture(coarse = true) {
    const root = new Element(); root.ownerDocument = { activeElement: null };
    root.setAttribute('contenteditable', 'true');
    const input = new Element();
    input.focus = () => { root.ownerDocument.activeElement = input; };
    input.blur = () => { root.ownerDocument.activeElement = null; };
    root.querySelectorAll = () => [input];
    const keyboard = installTerminalKeyboard({root, focus: input.focus, win: {matchMedia: () => ({matches: coarse})}});
    const event = (type, pointerType) => {
        const e = new Event(type, {cancelable: true});
        if (pointerType) Object.assign(e, {pointerType});
        root.dispatchEvent(e); return e;
    };
    return {root, input, keyboard, event};
}

test('touch taps and swipes stay non-editing, explicit Keyboard enables typing', () => {
    const {root, input, keyboard, event} = fixture();
    assert.equal(input.readOnly, true);
    assert.equal(root.getAttribute('contenteditable'), 'false');
    assert.equal(input.getAttribute('inputmode'), 'none');
    let gestures = 0;
    for (const type of ['touchstart', 'touchmove', 'touchend']) root.addEventListener(type, () => gestures++);
    // Simulate the renderer focusing its textarea after every touchend.
    root.addEventListener('touchend', input.focus);
    for (const type of ['touchstart', 'touchmove', 'touchend']) assert.equal(event(type).defaultPrevented, false);
    assert.equal(gestures, 3);
    assert.equal(root.ownerDocument.activeElement, input);
    assert.equal(input.readOnly, true);
    event('mousedown'); // Safari's compatibility event must not unlock typing.
    assert.equal(input.readOnly, true);
    keyboard.openKeyboard();
    assert.equal(input.readOnly, false);
    assert.equal(input.getAttribute('inputmode'), null);
    assert.equal(root.ownerDocument.activeElement, input);
    event('touchstart');
    assert.equal(input.readOnly, true);
    assert.equal(root.ownerDocument.activeElement, null);
});

test('desktop mouse focus works, including after touch on a hybrid device', () => {
    const {root, input, keyboard, event} = fixture(false);
    assert.equal(input.readOnly, false);
    event('pointerdown', 'touch');
    assert.equal(input.readOnly, true);
    event('pointerdown', 'mouse');
    assert.equal(input.readOnly, false);
    assert.equal(root.getAttribute('contenteditable'), 'true');
    keyboard.dispose(); keyboard.dispose();
    event('touchstart');
    assert.equal(input.readOnly, false);
    assert.equal(input.getAttribute('inputmode'), null);
});
