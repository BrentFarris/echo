import createDOMPurify from 'dompurify';

// Monaco 0.56 vendors an older sanitizer. Keep its hooks isolated while using
// the same patched dependency as Echo's Markdown rendering.
export default createDOMPurify(window);
