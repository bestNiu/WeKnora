import type { Metadata } from "next";
import "./globals.css";
import { themeInitializationScript } from "./theme";

export const metadata: Metadata = {
  title: "有临 YLKnow — 企业知识智能平台",
  description: "有临 YLKnow 是企业知识智能平台：RAG 问答、Agent 推理与自动 Wiki 于一体，支持私有化部署、精细的成员与知识库权限管理。",
  // Reuse the documentation favicon so the homepage adds nothing at the site root.
  icons: { icon: "/docs/favicon.ico" },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="zh-CN" className="h-full antialiased" suppressHydrationWarning>
      <head><script dangerouslySetInnerHTML={{ __html: themeInitializationScript }} /></head>
      <body className="min-h-full flex flex-col">{children}</body>
    </html>
  );
}
