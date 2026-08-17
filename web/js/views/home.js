// views/home.js — Echo base layout shell (non-functional).
//
// Renders the primary chat view with the left sidebar and terminal bar,
// matching the OLD (Wails) frontend's structure and styling. This is a visual
// shell only: no chat logic, no terminal functionality, no backend wiring.

import { icons } from "../icons.js";

function leftNav() {
  return `
    <aside class="left-nav" aria-label="Primary">
      <div class="left-nav-workspace">
        <button class="nav-icon-button workspace-dropdown-trigger" type="button" title="Select workspace" aria-label="Workspace selector" aria-expanded="false">
          <span class="workspace-icon-label">E</span>
        </button>
      </div>
      <nav class="left-nav-buttons" aria-label="Views">
        <button class="nav-icon-button is-active" type="button" title="Chat" aria-label="Chat">${icons.chat}</button>
        <button class="nav-icon-button" type="button" title="Kanban" aria-label="Kanban">${icons.kanban}</button>
      </nav>
      <div class="left-nav-actions">
        <button class="nav-icon-button" type="button" title="Code" aria-label="Code view">${icons.code}</button>
        <button class="nav-icon-button" type="button" title="Tasks" aria-label="Tasks">${icons.tasks}</button>
        <button class="nav-icon-button" type="button" title="Git" aria-label="Git">${icons.git}</button>
        <button class="nav-icon-button" type="button" title="Dashboard" aria-label="Dashboard">${icons.dashboard}</button>
        <button class="nav-icon-button" type="button" title="Settings" aria-label="Settings">${icons.settings}</button>
      </div>
    </aside>
  `;
}

function chatPanel() {
  return `
    <main class="main-content">
      <section class="workspace-panel">
        <section class="work-panel chat-panel" aria-label="Chat">
          <div class="chat-log" data-chat-log>
            <div class="empty-state chat-empty">Ask Echo to inspect, plan, or break down work for this workspace.</div>
          </div>
          <form class="chat-composer" data-chat-form>
            <div class="chat-composer-main" data-chat-input-wrap>
              <div
                class="chat-composer-editor"
                contenteditable="true"
                role="textbox"
                aria-multiline="true"
                aria-label="Message Echo"
                spellcheck="true"
                data-chat-input
                data-placeholder="Describe what to build"
              ></div>
            </div>
            <div class="chat-composer-toolbar">
              <div class="chat-composer-toolbar-left">
                <button class="chat-toolbar-icon" type="button" title="Attach file" aria-label="Attach file">${icons.plus}</button>
                <button class="chat-toolbar-icon chat-speech-recognition" type="button" title="Hold to speak" aria-label="Voice input">${icons.mic}</button>
                <button class="model-selector chat-toolbar-model" type="button" title="Select model" aria-haspopup="listbox" aria-expanded="false">
                  <span class="model-selector-label">Model</span>
                  <span class="model-selector-chevron">${icons.arrowDown}</span>
                </button>
                <button class="model-selector mode-selector chat-toolbar-mode" type="button" title="Agent mode" aria-haspopup="listbox" aria-expanded="false">
                  <span class="model-selector-label">Mode</span>
                  <span class="model-selector-chevron">${icons.arrowDown}</span>
                </button>
                <span class="chat-toolbar-separator"></span>
                <button class="chat-toolbar-icon execute-button" type="button" title="Execute plan" aria-label="Execute plan">${icons.execute}</button>
                <span class="chat-toolbar-separator"></span>
                <button class="chat-toolbar-icon" type="button" title="More options" aria-label="More options">${icons.moreHorizontal}</button>
              </div>
              <div class="chat-composer-toolbar-right">
                <button class="send-button" type="button" title="Send" aria-label="Send message">${icons.send}</button>
              </div>
            </div>
          </form>
        </section>
      </section>
    </main>
  `;
}

function terminalDock() {
  return `
    <section class="terminal-dock" data-terminal-dock aria-label="Integrated terminal">
      <header class="terminal-toolbar">
        <button class="terminal-title-button" type="button" aria-expanded="false">
          ${icons.terminal}
          <span class="terminal-title">Terminal</span>
          <span class="terminal-session-label">pwsh</span>
          <span class="terminal-status-indicator is-running" title="Terminal ready"></span>
          <span class="terminal-status-text">ready</span>
        </button>
        <div class="terminal-toolbar-actions">
          <button class="terminal-toolbar-button" type="button" title="Saved commands" aria-label="Saved commands">${icons.star}<span>Saved</span></button>
          <button class="terminal-toolbar-button icon-only" type="button" title="Restart terminal" aria-label="Restart terminal">${icons.refresh}</button>
          <button class="terminal-toolbar-button icon-only danger" type="button" title="Kill terminal" aria-label="Kill terminal">${icons.trash}</button>
          <button class="terminal-toolbar-button icon-only terminal-maximize-button" type="button" title="Maximize terminal" aria-label="Maximize terminal">${icons.expand}</button>
          <button class="terminal-toolbar-button icon-only" type="button" title="Open terminal" aria-label="Open terminal">${icons.arrowUp}</button>
        </div>
      </header>
    </section>
  `;
}

export function mount(root) {
  root.innerHTML = `
    <div class="app-shell">
      <div data-region="left-nav">${leftNav()}</div>
      <div data-region="main">${chatPanel()}</div>
      <div data-region="terminal">${terminalDock()}</div>
    </div>
  `;
}

export function unmount() {
  // No listeners to clean up — the base layout is non-functional.
}
