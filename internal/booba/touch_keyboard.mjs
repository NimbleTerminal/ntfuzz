/**
 * Host policy for an explicit mobile keyboard. Renderer touch handlers may still
 * focus the input, but cannot open an editable surface until Keyboard is chosen.
 * Do not cancel touch events: scrolling and terminal mouse input must still run.
 */
export function installTerminalKeyboard({ root, focus, win = window }) {
    const originalEditable = root.getAttribute('contenteditable');
    const inputs = new Map();
    let disposed = false;
    const rememberInputs = () => {
        for (const input of root.querySelectorAll('textarea')) {
            if (!inputs.has(input)) inputs.set(input, {
                readOnly: input.readOnly,
                inputmode: input.getAttribute('inputmode'),
            });
        }
    };
    const restoreAttribute = (element, name, value) => {
        if (value === null) element.removeAttribute(name);
        else element.setAttribute(name, value);
    };
    const enable = () => {
        rememberInputs();
        restoreAttribute(root, 'contenteditable', originalEditable);
        for (const [input, prior] of inputs) {
            input.readOnly = prior.readOnly;
            restoreAttribute(input, 'inputmode', prior.inputmode);
        }
    };
    const browse = () => {
        if (disposed) return;
        rememberInputs();
        // The renderer also marks its host contenteditable for extension support.
        root.setAttribute('contenteditable', 'false');
        for (const input of inputs.keys()) {
            input.readOnly = true;
            input.setAttribute('inputmode', 'none');
            if (root.ownerDocument.activeElement === input) input.blur();
        }
    };
    const pointer = event => {
        if (event.pointerType === 'touch' || event.pointerType === 'pen') browse();
        else if (event.pointerType === 'mouse') enable();
    };
    root.addEventListener('pointerdown', pointer, { capture: true, passive: true });
    // Safari touch fallback; capture runs before canvas touchend focus handlers.
    root.addEventListener('touchstart', browse, { capture: true, passive: true });
    if (win.matchMedia('(pointer: coarse)').matches) browse();
    return {
        openKeyboard() {
            if (disposed) return;
            // Refocus synchronously within the button gesture, including when a
            // prior canvas tap already focused the read-only input (iOS).
            for (const input of inputs.keys()) {
                if (root.ownerDocument.activeElement === input) input.blur();
            }
            enable();
            focus();
        },
        dispose() {
            if (disposed) return;
            root.removeEventListener('pointerdown', pointer, true);
            root.removeEventListener('touchstart', browse, true);
            enable();
            inputs.clear();
            disposed = true;
        },
    };
}
