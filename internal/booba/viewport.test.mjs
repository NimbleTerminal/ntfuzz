import { test } from 'node:test';
import assert from 'node:assert/strict';
import { installTerminalViewport } from './viewport.mjs';
function fixture(visual = true) {
    const win = new EventTarget();
    const frames = new Map(); let id = 0, fits = 0;
    win.innerHeight = 900;
    win.requestAnimationFrame = fn => { frames.set(++id, fn); return id; };
    win.cancelAnimationFrame = id => frames.delete(id);
    if (visual) Object.assign(win, {visualViewport: Object.assign(new EventTarget(), {height: 750, scale: 1, offsetTop: 0})});
    const values = new Map();
    const root = {style: {getPropertyValue: k => values.get(k) ?? '', setProperty: (k,v) => values.set(k,v), removeProperty: k => values.delete(k)}};
    const handle = installTerminalViewport({root, win, fit: () => fits++});
    const flush = () => { const pending = [...frames.values()]; frames.clear(); pending.forEach(fn => fn()); };
    return {win, values, handle, flush, fits: () => fits};
}
test('keyboard / toolbar changes coalesce and refit without reconnecting', () => {
    const f=fixture();f.flush();assert.equal(f.values.get('--terminal-viewport-height'),'750px');
    f.win.visualViewport.height=380;f.win.visualViewport.offsetTop=25;
    f.win.visualViewport.dispatchEvent(new Event('resize'));f.win.visualViewport.dispatchEvent(new Event('scroll'));
    f.flush();assert.equal(f.fits(),2);assert.equal(f.values.get('--terminal-viewport-height'),'380px');assert.equal(f.values.get('--terminal-viewport-top'),'25px');
    f.win.visualViewport.height=750;f.win.visualViewport.offsetTop=0;f.win.dispatchEvent(new Event('orientationchange'));f.flush();assert.equal(f.values.get('--terminal-viewport-height'),'750px');
});
test('pinch zoom does not reflow the terminal', () => {
    const f=fixture();f.flush();f.win.visualViewport.scale=2;f.win.visualViewport.height=300;f.win.visualViewport.dispatchEvent(new Event('resize'));f.flush();assert.equal(f.fits(),1);assert.equal(f.values.get('--terminal-viewport-height'),'750px');
});
test('fallback and cleanup', () => {
    const f=fixture(false);f.flush();assert.equal(f.values.get('--terminal-viewport-height'),'900px');
    f.handle.fit();f.handle.dispose();f.handle.dispose();f.flush();f.win.dispatchEvent(new Event('resize'));f.flush();assert.equal(f.fits(),1);assert.equal(f.values.size,0);
});
