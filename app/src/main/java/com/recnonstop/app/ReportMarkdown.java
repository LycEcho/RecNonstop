package com.recnonstop.app;

import android.content.Context;
import android.graphics.Typeface;
import android.text.SpannableStringBuilder;
import android.text.Spanned;
import android.text.style.BackgroundColorSpan;
import android.text.style.ClickableSpan;
import android.text.TextPaint;
import android.view.View;
import android.text.style.ForegroundColorSpan;
import android.text.style.LeadingMarginSpan;
import android.text.style.RelativeSizeSpan;
import android.text.style.StyleSpan;
import android.text.style.TypefaceSpan;

import java.util.regex.Matcher;
import java.util.regex.Pattern;

/** 日报常用 Markdown 的本地排版，不执行 HTML、不加载外部资源。 */
final class ReportMarkdown {
    private static final Pattern HEADING = Pattern.compile("^(#{1,6})\\s+(.+?)(?:\\s+#+)?$");
    private static final Pattern TASK = Pattern.compile("^\\s*[-*+]\\s+\\[([ xX])\\]\\s+(.*)$");
    private static final Pattern INLINE = Pattern.compile("\\*\\*(.+?)\\*\\*|__(.+?)__|`([^`]+)`");

    private ReportMarkdown() {}

    // Only used with labels supplied by structured report detail. The Markdown
    // parser never turns arbitrary model-written dates or URLs into actions.
    static CharSequence sourceLink(Context context, String label, Runnable open) {
        SpannableStringBuilder text = new SpannableStringBuilder(label);
        if (text.length() == 0) return text;
        text.setSpan(new ClickableSpan() {
            @Override public void onClick(View widget) { open.run(); }
            @Override public void updateDrawState(TextPaint paint) {
                paint.setColor(context.getColor(R.color.brand));
                paint.setUnderlineText(true);
            }
        }, 0, text.length(), Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
        return text;
    }

    static CharSequence render(Context context, String markdown) {
        SpannableStringBuilder out = new SpannableStringBuilder();
        if (markdown == null || markdown.trim().isEmpty()) return out;
        boolean code = false;
        int indent = Math.round(14 * context.getResources().getDisplayMetrics().density);
        for (String raw : markdown.replace("\r\n", "\n").replace('\r', '\n').trim().split("\n", -1)) {
            String line = raw.trim();
            if (line.startsWith("```")) {
                code = !code;
                continue;
            }
            if (code) {
                int start = out.length();
                out.append(raw).append('\n');
                span(out, new TypefaceSpan("monospace"), start);
                span(out, new BackgroundColorSpan(context.getColor(R.color.neutral_light)), start);
                continue;
            }
            if (line.matches("([-*_])(?:\\s*\\1){2,}")) {
                out.append('\n');
                continue;
            }
            Matcher heading = HEADING.matcher(line);
            if (heading.matches()) {
                if (out.length() > 0 && out.charAt(out.length() - 1) == '\n'
                        && (out.length() < 2 || out.charAt(out.length() - 2) != '\n')) out.append('\n');
                int start = out.length();
                String title = heading.group(2);
                appendInline(out, title);
                span(out, new StyleSpan(Typeface.BOLD), start);
                span(out, new RelativeSizeSpan(heading.group(1).length() == 1 ? 1.35f : 1.15f), start);
                int color = title.contains("风险") || title.contains("待确认")
                        ? R.color.warning : R.color.text_primary;
                span(out, new ForegroundColorSpan(context.getColor(color)), start);
                out.append('\n');
                continue;
            }
            boolean list = line.matches("^[-*+]\\s+.*") || line.matches("^\\d+[.)]\\s+.*");
            boolean quote = line.startsWith(">");
            Matcher task = TASK.matcher(line);
            if (task.matches()) {
                line = (task.group(1).equals(" ") ? "☐  " : "☑  ") + task.group(2);
                list = true;
            } else if (list) {
                line = line.replaceFirst("^[-*+]\\s+", "•  ");
            } else if (quote) {
                line = line.replaceFirst("^>\\s?", "");
            }
            int start = out.length();
            appendInline(out, line);
            if (quote) span(out, new ForegroundColorSpan(context.getColor(R.color.text_secondary)), start);
            out.append('\n');
            if (list || quote) {
                span(out, new LeadingMarginSpan.Standard(quote ? indent : 0, indent), start);
            }
        }
        while (out.length() > 0 && out.charAt(out.length() - 1) == '\n') out.delete(out.length() - 1, out.length());
        return out;
    }

    private static void appendInline(SpannableStringBuilder out, String line) {
        Matcher matcher = INLINE.matcher(line);
        int from = 0;
        while (matcher.find()) {
            out.append(line, from, matcher.start());
            int start = out.length();
            boolean code = matcher.group(3) != null;
            out.append(code ? matcher.group(3) : matcher.group(1) != null ? matcher.group(1) : matcher.group(2));
            span(out, code ? new TypefaceSpan("monospace") : new StyleSpan(Typeface.BOLD), start);
            from = matcher.end();
        }
        out.append(line, from, line.length());
    }

    private static void span(SpannableStringBuilder out, Object style, int start) {
        if (out.length() > start) out.setSpan(style, start, out.length(), Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
    }
}
