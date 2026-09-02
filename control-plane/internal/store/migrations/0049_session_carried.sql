-- 停止中のセッション行にも「答えを待っている対話がある」ことを出すための列（docs/log/75）。
-- 値は畳まれたときに画面に出ていた種類（question | plan | permission）で、無ければ空。
--
-- なぜ DB ミラーに要るか: Workspace が停止していると Agent には誰も聞けず、CP はこの
-- ミラーからセッション一覧を作る。列が無ければ、停止中の一覧は 停止中 の 1 語だけになり、
-- 未回答の質問を抱えたセッションと、ただ畳まれただけのセッションが区別できない。
ALTER TABLE session ADD COLUMN carried TEXT NOT NULL DEFAULT '';
