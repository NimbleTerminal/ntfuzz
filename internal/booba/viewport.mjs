/**
 * Prototype host boundary: size an app shell to the unobscured visual viewport,
 * then ask its terminal to measure itself. No columns, font scaling, reconnects,
 * or application layout decisions belong here. Pinch zoom keeps native behavior.
 */
export function installTerminalViewport({ root, fit, win = window }) {
    const viewport = win.visualViewport;
    const priorHeight = root.style.getPropertyValue('--terminal-viewport-height');
    const priorTop = root.style.getPropertyValue('--terminal-viewport-top');
    let frame = 0;
    let disposed = false;
    const update = () => {
        frame = 0;
        if (disposed) return;
        // Resizing the grid while pinching makes content chase the user's fingers.
        if (viewport && Math.abs(viewport.scale - 1) > 0.01) return;
        const height = viewport?.height ?? win.innerHeight;
        if (height > 0) root.style.setProperty('--terminal-viewport-height', `${Math.round(height)}px`);
        root.style.setProperty('--terminal-viewport-top', `${Math.round(viewport?.offsetTop ?? 0)}px`);
        fit();
    };
    const schedule = () => {
        if (!disposed && !frame) frame = win.requestAnimationFrame(update);
    };
    viewport?.addEventListener('resize', schedule);
    viewport?.addEventListener('scroll', schedule);
    win.addEventListener('resize', schedule);
    win.addEventListener('orientationchange', schedule);
    schedule();
    return {
        fit: schedule,
        dispose() {
            if (disposed) return;
            disposed = true;
            win.cancelAnimationFrame(frame);
            viewport?.removeEventListener('resize', schedule);
            viewport?.removeEventListener('scroll', schedule);
            win.removeEventListener('resize', schedule);
            win.removeEventListener('orientationchange', schedule);
            if (priorHeight) root.style.setProperty('--terminal-viewport-height', priorHeight);
            else root.style.removeProperty('--terminal-viewport-height');
            if (priorTop) root.style.setProperty('--terminal-viewport-top', priorTop);
            else root.style.removeProperty('--terminal-viewport-top');
        },
    };
}
