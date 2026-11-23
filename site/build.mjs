// Builds the static site into site/public: the landing page, /docs from docs/MANUAL.md, its screenshots,
// robots.txt and sitemap.xml. No dependencies: node site/build.mjs
import { readFileSync, writeFileSync, mkdirSync, rmSync, cpSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..");
const out = join(here, "public");
const SITE = "https://ownshopai.store";

rmSync(out, { recursive: true, force: true });
mkdirSync(join(out, "docs", "manual"), { recursive: true });
cpSync(join(here, "src", "index.html"), join(out, "index.html"));
cpSync(join(here, "src", "styles.css"), join(out, "styles.css"));
for (const f of readdirSync(join(root, "docs", "manual"))) {
  if (f.endsWith(".png")) cpSync(join(root, "docs", "manual", f), join(out, "docs", "manual", f));
}

const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
const slug = (s) => s.toLowerCase().replace(/[^a-z0-9 -]/g, "").trim().replace(/ +/g, "-");

function inline(s) {
  const codes = [];
  s = s.replace(/`([^`]+)`/g, (_, c) => `\u0000${codes.push(esc(c)) - 1}\u0000`);
  s = esc(s);
  s = s.replace(/!\[([^\]]*)\]\(([^)]+)\)/g, (_, alt, src) => `<img src="${src}" alt="${alt}" loading="lazy">`);
  s = s.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2">$1</a>');
  s = s.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  s = s.replace(/\u0000(\d+)\u0000/g, (_, i) => `<code>${codes[i]}</code>`);
  return s;
}

function render(md) {
  const lines = md.split("\n");
  const html = [];
  const toc = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (line.startsWith("```")) {
      const buf = [];
      i++;
      while (i < lines.length && !lines[i].startsWith("```")) buf.push(lines[i++]);
      i++;
      html.push(`<pre><code>${esc(buf.join("\n"))}</code></pre>`);
      continue;
    }
    const h = line.match(/^(#{1,3}) (.*)$/);
    if (h) {
      const level = h[1].length;
      const id = slug(h[2]);
      if (level === 2 && h[2] !== "Contents") toc.push({ id, text: h[2] });
      if (h[2] !== "Contents") html.push(`<h${level} id="${id}">${inline(h[2])}</h${level}>`);
      if (h[2] === "Contents") { i++; while (i < lines.length && !lines[i].startsWith("## ")) i++; continue; }
      i++;
      continue;
    }
    const img = line.match(/^!\[([^\]]*)\]\(([^)]+)\)$/);
    if (img) {
      html.push(`<figure><img src="${img[2]}" alt="${esc(img[1])}" loading="lazy"><figcaption>${esc(img[1])}</figcaption></figure>`);
      i++;
      continue;
    }
    if (line.startsWith("|")) {
      const rows = [];
      while (i < lines.length && lines[i].startsWith("|")) rows.push(lines[i++]);
      const cells = (r) => r.replace(/^\||\|$/g, "").split("|").map((c) => c.trim());
      const head = cells(rows[0]).map((c) => `<th>${inline(c)}</th>`).join("");
      const body = rows.slice(2).map((r) => `<tr>${cells(r).map((c) => `<td>${inline(c)}</td>`).join("")}</tr>`).join("");
      html.push(`<table><thead><tr>${head}</tr></thead><tbody>${body}</tbody></table>`);
      continue;
    }
    if (/^(\d+\.|-) /.test(line)) {
      const ordered = /^\d+\./.test(line);
      const start = ordered ? parseInt(line, 10) : 1;
      const items = [];
      while (i < lines.length && /^(\d+\.|-) /.test(lines[i])) items.push(lines[i++].replace(/^(\d+\.|-) /, ""));
      const tag = ordered ? "ol" : "ul";
      html.push(`<${tag}${ordered && start > 1 ? ` start="${start}"` : ""}>${items.map((t) => `<li>${inline(t)}</li>`).join("")}</${tag}>`);
      continue;
    }
    if (line.startsWith("> ")) {
      const buf = [];
      while (i < lines.length && lines[i].startsWith(">")) buf.push(lines[i++].replace(/^> ?/, ""));
      html.push(`<div class="note"><p>${inline(buf.join(" "))}</p></div>`);
      continue;
    }
    if (line.trim() === "") { i++; continue; }
    const para = [];
    while (i < lines.length && lines[i].trim() !== "" && !/^(#|```|\||!\[|> |(\d+\.|-) )/.test(lines[i])) para.push(lines[i++]);
    html.push(`<p>${inline(para.join(" "))}</p>`);
  }
  return { body: html.join("\n"), toc };
}

const index = readFileSync(join(here, "src", "index.html"), "utf8");
const head = index.slice(0, index.indexOf("<title>"));
const nav = index.slice(index.indexOf('<header class="nav">'), index.indexOf("</header>") + 9).replace(/href="#/g, 'href="/#');
const footer = index.slice(index.indexOf("<footer>"));
const favicon = index.match(/<link rel="icon"[^>]*>/)[0];
const page = (title, desc, path, inner) => `${head}<title>${esc(title)}</title>
<meta name="description" content="${esc(desc)}">
<link rel="canonical" href="${SITE}${path}">
${favicon}
<link rel="stylesheet" href="/styles.css">
</head>
<body>
${nav}
${inner}
${footer}`;
const urls = ["/"];

// /docs: the one-page manual
{
  const { body, toc } = render(readFileSync(join(root, "docs", "MANUAL.md"), "utf8"));
  writeFileSync(join(out, "docs", "index.html"), page("OwnShop AI docs - user manual",
    "How to install OwnShop AI, set up your shop, connect WhatsApp and Telegram, add products or services, take payments and manage orders.", "/docs/",
    `<div class="doc-layout">
  <aside class="toc"><b>User manual</b><ol>${toc.map((t) => `<li><a href="#${t.id}">${esc(t.text)}</a></li>`).join("")}</ol></aside>
  <article class="prose">
${body}
  </article>
</div>`));
  urls.push("/docs/");
}

// /manual: one page per how-to guide from docs/guides/NN-slug.md, plus an index with cards
{
  const files = readdirSync(join(root, "docs", "guides")).filter((f) => /^\d\d-.*\.md$/.test(f)).sort();
  const guides = files.map((f) => {
    const md = readFileSync(join(root, "docs", "guides", f), "utf8");
    const title = md.match(/^# (.*)$/m)[1];
    const lede = md.split("\n").find((l, k, a) => k > 0 && l.trim() && !l.startsWith("#"));
    return { slug: f.slice(3, -3), n: parseInt(f, 10), title, lede: lede.replace(/[`*]/g, ""), md };
  });
  const side = (cur) => `<aside class="toc man"><b><a href="/manual/" style="padding:0;color:inherit">Manual</a></b><ol>${guides.map((g) =>
    `<li><a href="/manual/${g.slug}/"${g.slug === cur ? ' class="on" aria-current="page"' : ""}>${g.n}. ${esc(g.title)}</a></li>`).join("")}</ol></aside>`;
  guides.forEach((g, k) => {
    const { body, toc } = render(g.md);
    const prev = guides[k - 1], next = guides[k + 1];
    const onPage = toc.length > 2 ? `<nav class="onpage"><b>On this page</b>${toc.map((t) => `<a href="#${t.id}">${esc(t.text)}</a>`).join("")}</nav>` : "";
    const pn = `<nav class="pn">${prev ? `<a class="prev" href="/manual/${prev.slug}/"><span>Previous</span>${prev.n}. ${esc(prev.title)}</a>` : "<span></span>"}${next ? `<a class="next" href="/manual/${next.slug}/"><span>Next</span>${next.n}. ${esc(next.title)}</a>` : ""}</nav>`;
    const html = body.replace(/(<h1[^>]*>.*?<\/h1>)/, `<p class="crumb"><a href="/manual/">Manual</a> / Guide ${g.n}</p>$1`).replace(/(<\/h1>\n<p>.*?<\/p>)/, `$1\n${onPage}`);
    mkdirSync(join(out, "manual", g.slug), { recursive: true });
    writeFileSync(join(out, "manual", g.slug, "index.html"), page(`${g.title} - OwnShop AI manual`, g.lede, `/manual/${g.slug}/`,
      `<div class="doc-layout">\n${side(g.slug)}\n<article class="prose">\n${html}\n${pn}\n</article>\n</div>`));
    urls.push(`/manual/${g.slug}/`);
  });
  mkdirSync(join(out, "manual"), { recursive: true });
  writeFileSync(join(out, "manual", "index.html"), page("OwnShop AI manual - step-by-step guides",
    "Step-by-step guides for OwnShop AI: install on Windows or Mac, set up your shop, add products from Excel, bookings, payments, WhatsApp, Telegram, daily use and troubleshooting.", "/manual/",
    `<div class="doc-layout">\n${side("")}\n<article class="prose manual-home">
<h1>Manual</h1>
<p>Step-by-step guides for OwnShop AI, from the download to your daily orders. Each guide has numbered steps, what you should see, screenshots, and what to do if it goes wrong. Start with guide 1 or 2, then 3.</p>
<div class="guide-cards">${guides.map((g) => `<a href="/manual/${g.slug}/"><span class="gn">${g.n}</span><b>${esc(g.title)}</b><span>${esc(g.lede)}</span></a>`).join("")}</div>
<p>Prefer one long page? The <a href="/docs/">user manual on one page</a> has the same information in short form.</p>
</article>\n</div>`));
  urls.splice(1, 0, "/manual/");
}

// /credits
{
  const inner = readFileSync(join(here, "src", "credits.html"), "utf8");
  mkdirSync(join(out, "credits"), { recursive: true });
  writeFileSync(join(out, "credits", "index.html"), page("Credits - OwnShop AI", "The open source software and models OwnShop AI is built on, with their licenses.", "/credits/", inner));
  urls.push("/credits/");
}

// /privacy and /terms
for (const [slugp, title, desc] of [["privacy", "Privacy Policy - OwnShop AI", "What personal data OwnShop AI, the free shop link service and this website handle, and your rights."],
  ["terms", "Terms of Service - OwnShop AI", "Terms for the OwnShop AI software, the free shop link service and acceptable use."]]) {
  mkdirSync(join(out, slugp), { recursive: true });
  writeFileSync(join(out, slugp, "index.html"), page(title, desc, `/${slugp}/`, readFileSync(join(here, "src", `${slugp}.html`), "utf8")));
  urls.push(`/${slugp}/`);
}

// /download/<file>/: one page per installer; the analytics script reports a download event there.
{
  const files = {
    "windows-x64": ["Windows x64", "ownshop-setup-windows-amd64.exe", "/manual/install-windows/"],
    "windows-arm64": ["Windows on ARM", "ownshop-setup-windows-arm64.exe", "/manual/install-windows/"],
    mac: ["Mac", "ownshop-macos-arm64.dmg", "/manual/install-mac/"],
  };
  for (const [k, [label, file, guide]] of Object.entries(files)) {
    const url = `https://downloads.deemwar.com/ownshopai/${file}`;
    mkdirSync(join(out, "download", k), { recursive: true });
    writeFileSync(join(out, "download", k, "index.html"), page(`Download OwnShop AI for ${label}`, `Downloading OwnShop AI for ${label}.`, `/download/${k}/`,
      `<section><div class="wrap narrow">
<h2>Your download is starting</h2>
<p class="section-lede">OwnShop AI for ${label}: <code>${file}</code>, about 700 MB. If it does not start, <a href="${url}">click here</a>.</p>
<p>Next: follow the <a href="${guide}">install guide</a>, then <a href="/manual/first-setup/">First setup</a>.</p>
</div></section>
<script>setTimeout(function () { location.href = ${JSON.stringify(url)}; }, 600);</script>`).replace("</head>", '<meta name="robots" content="noindex">\n</head>'));
  }
}

writeFileSync(join(out, "robots.txt"), `User-agent: *\nAllow: /\nSitemap: ${SITE}/sitemap.xml\n`);
writeFileSync(join(out, "sitemap.xml"), `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
${urls.map((u) => `<url><loc>${SITE}${u}</loc></url>`).join("\n")}
</urlset>
`);
writeFileSync(join(out, "_headers"), `/*
  X-Content-Type-Options: nosniff
  Referrer-Policy: strict-origin-when-cross-origin
/docs/manual/*
  Cache-Control: public, max-age=86400
`);
console.log(`built ${out}: ${urls.length} pages`);
